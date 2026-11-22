package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func offlineRetryable(status int, err error) bool {
	var cert *tls.CertificateVerificationError
	var invalid *admissionProtocolError
	if errors.As(err, &cert) || errors.As(err, &invalid) {
		return false
	}
	return status == 408 || status == 429 || status >= 500
}

type admissionProtocolError struct{}

func (*admissionProtocolError) Error() string { return "invalid control response" }

// The caller owns offlineMu. No SQLite or sampling lock spans a WAN request.
func (a *Agent) syncOfflineState(ctx context.Context, s *offlineState, force bool) error {
	now := a.now()
	if s.Grant.Closed {
		return nil
	}
	if now.Before(s.LastWall) {
		s.Denied = true
	}
	var next model.OfflineUsage
	if s.Pending == nil {
		u, err := s.usage(now)
		if err != nil {
			return err
		}
		u.SlotAction = s.SlotAction
		if a.nodeQuotaEnabled() {
			u.RetainedNormalUnits, err = retainedLegacyUnits(s)
			if err != nil {
				return err
			}
		}
		// Reconnects and explicit slot transitions still check control. Only
		// periodic, unchanged reports for proven-idle grants may sleep.
		if !force && s.idleReportConfirmed(u) {
			return nil
		}
		next = u
	}
	if !force && now.Before(s.RetryAt) {
		if s.Denied {
			return errOfflineDenied
		}
		return nil
	}
	if s.Pending == nil {
		s.NextVersion = next.Version
		s.Pending = &next
		if err := a.ledger.saveOffline(*s); err != nil {
			return err
		}
	}
	u := *s.Pending
	status, data, err := a.requestAdmission(ctx, "/api/v1/agents/offline-grants/"+s.Grant.ID+"/report", u)
	if status == 200 && err == nil {
		var g model.OfflineGrant
		if json.Unmarshal(data, &g) != nil || !offlineReceiptValid(s, g, u, now) {
			err = &admissionProtocolError{}
		} else {
			s.Grant = g
			if err = s.rememberBilling(); err != nil {
				return err
			}
			s.AcknowledgedReport = offlineReportDigest(u)
			s.Pending = nil
			if u.SlotAction == s.SlotAction {
				s.SlotAction = ""
			}
			s.Online = true
			s.Failures = 0
			s.RetryAt = now.Add(5 * time.Second)
			s.Denied = s.Denied || g.Revoked || g.Closed
			return a.ledger.saveOffline(*s)
		}
	}
	s.Online = false
	if !offlineRetryable(status, err) {
		s.Denied = true
	}
	s.Failures = min(s.Failures+1, 4)
	s.RetryAt = now.Add(time.Duration(min(30, 2<<s.Failures)) * time.Second)
	if saveErr := a.ledger.saveOffline(*s); saveErr != nil {
		return saveErr
	}
	if s.Denied {
		return errOfflineDenied
	}
	return nil
}

func (a *Agent) offlineRenew(w http.ResponseWriter, r *http.Request, id string, u model.LeaseUsage) {
	a.offlineMu.Lock()
	defer a.offlineMu.Unlock()
	s, found, err := a.ledger.offlineChild(id)
	if err != nil {
		http.Error(w, "offline ledger unavailable", 503)
		return
	}
	if !found {
		http.Error(w, "unknown offline lease", 403)
		return
	}
	if err = s.updateChild(id, u, a.now()); err != nil {
		http.Error(w, "offline counters rejected", 409)
		return
	}
	if err = a.ledger.saveOffline(s); err != nil {
		http.Error(w, "offline usage persistence failed", 503)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), admissionControlTimeout)
	defer cancel()
	// At a normal-budget boundary, determine whether control is actually
	// unavailable before deciding whether the emergency tail may be used.
	child := s.Children[id]
	force := s.Online && child.Normal-child.Upload-child.Download < model.OfflineChunkBytes/4
	if s.Grant.Billing != nil {
		force = s.Online && (child.Normal-child.NormalUsed)/s.Grant.Billing.Multiplier < model.OfflineChunkBytes/4
	}
	err = a.syncOfflineState(ctx, &s, force || u.Closed)
	if u.Closed {
		if err == nil {
			_ = a.releaseIdleOfflineSlot(ctx, &s)
		}
		delete(a.offlineIssued, id)
		// Local durable receipt is sufficient for a child final. The parent
		// report is retained until the control service confirms exact totals.
		data, _ := json.Marshal(model.ConnectionLease{LeaseID: id, UploadBytes: u.UploadBytes, DownloadBytes: u.DownloadBytes, Closed: true})
		_ = writeAdmissionResponse(w, 200, data, nil)
		return
	}
	if err != nil {
		status := 503
		if errors.Is(err, errOfflineDenied) {
			status = 403
		}
		http.Error(w, "offline authorization unavailable", status)
		return
	}
	if s.Online && s.Children[id].Emergency > 0 {
		http.Error(w, "emergency authorization ended", 403)
		return
	}
	grant, err := a.localOfflineGrant(&s, id, a.now())
	if err == nil {
		if a.offlineIssued == nil {
			a.offlineIssued = map[string]time.Time{}
			a.offlineBoot = time.Now()
		}
		a.offlineIssued[id] = time.Now()
	}
	if saveErr := a.ledger.saveOffline(s); saveErr != nil {
		http.Error(w, "offline lease persistence failed", 503)
		return
	}
	if err != nil {
		http.Error(w, "offline authorization expired or budget exhausted", 403)
		return
	}
	data, _ := json.Marshal(grant)
	_ = writeAdmissionResponse(w, 200, data, nil)
}

func (a *Agent) offlineAllowed() bool {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	return a.cache.OfflineAdmissionVersion == model.OfflineAdmissionVersion
}

func isOfflineLease(id string) bool { return strings.HasPrefix(id, "off_") }

// Existing grants survive disabling issuance for a staged rollback.
func (a *Agent) cachedOfflineCredential(password string) bool {
	states, err := a.ledger.offlineStates()
	if err != nil {
		return true
	} // route to fail-closed persistent path
	hash := secure.Hash(password)
	for _, s := range states {
		if s.Grant.CredentialHash == hash && !s.Grant.Closed {
			return true
		}
	}
	return false
}

// Never remove the Agent or its ledger until all offline finals are confirmed.
func (a *Agent) flushOfflineRetirement(ctx context.Context) error {
	if err := a.syncOfflineGrants(ctx); err != nil {
		return err
	}
	a.offlineMu.Lock()
	defer a.offlineMu.Unlock()
	requests, err := a.ledger.offlineRequests("")
	if err != nil {
		return err
	}
	if len(requests) > 0 {
		return errors.New("offline admission outcome pending; ledger retained")
	}
	states, err := a.ledger.offlineStates()
	if err != nil {
		return err
	}
	for _, s := range states {
		if !s.Grant.Closed {
			return errors.New("offline grant settlement pending; ledger retained")
		}
	}
	return nil
}

// The core caps every child at request-start + 120 seconds on its monotonic
// clock. After our last issuance (or restart) plus 125 seconds without renewal,
// that particular child cannot still forward, even when another child on the
// same subscription is active. A core restart also proves termination. Neither
// observation fabricates final counters or returns unknown delegated bytes.
func (a *Agent) reconcileOfflineChildren(ctx context.Context, s *offlineState, generation string) {
	for id, child := range s.Children {
		if child.Closed || child.Ended {
			continue
		}
		ended := generation != "" && child.CoreGeneration != "" && child.CoreGeneration != generation
		issued, ok := a.offlineIssued[id]
		if !ok {
			issued = a.offlineBoot
		}
		if !ended && !issued.IsZero() && time.Since(issued) >= 125*time.Second {
			ended = true
		}
		if ended {
			child.Ended = true
			s.Children[id] = child
		}
	}
}

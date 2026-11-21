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
	if !force && now.Before(s.RetryAt) {
		if s.Denied {
			return errOfflineDenied
		}
		return nil
	}
	if s.Pending == nil {
		u, err := s.usage(now)
		if err != nil {
			return err
		}
		s.NextVersion = u.Version
		s.Pending = &u
		if err = a.ledger.saveOffline(*s); err != nil {
			return err
		}
	}
	u := *s.Pending
	status, data, err := a.requestAdmission(ctx, "/api/v1/agents/offline-grants/"+s.Grant.ID+"/report", u)
	if status == 200 && err == nil {
		var g model.OfflineGrant
		if json.Unmarshal(data, &g) != nil || g.ID != s.Grant.ID || g.NodeID != s.Grant.NodeID || g.CredentialHash != s.Grant.CredentialHash || g.UserID != s.Grant.UserID || g.SubscriptionID != s.Grant.SubscriptionID || g.SessionID != s.Grant.SessionID || g.AuthorizedAt.Before(s.Grant.AuthorizedAt) || g.OfflineUntil.Before(s.Grant.OfflineUntil) || g.ReportVersion != u.Version || g.UploadBytes != u.UploadBytes || g.DownloadBytes != u.DownloadBytes || g.EmergencyUsedBytes != u.EmergencyUsedBytes || g.Closed != u.Closed || g.NormalBudgetBytes < s.Grant.NormalBudgetBytes || g.EmergencyBudgetBytes != s.Grant.EmergencyBudgetBytes || g.RateVersion < s.Grant.RateVersion || g.NormalUnlimited != s.Grant.NormalUnlimited || (!g.Closed && !g.Revoked && !validOfflineGrant(g, a.cfg.NodeID, g.CredentialHash, now)) {
			err = &admissionProtocolError{}
		} else {
			if s.Grant.Billing != nil && (g.Billing == nil || g.Billing.Protocol != s.Grant.Billing.Protocol || g.Billing.Version < s.Grant.Billing.Version || g.Billing.BudgetUnits < s.Grant.Billing.BudgetUnits) {
				return &admissionProtocolError{}
			}
			s.Grant = g
			if err = s.rememberBilling(); err != nil {
				return err
			}
			s.Pending = nil
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
	err = a.syncOfflineState(ctx, &s, force)
	if u.Closed {
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
	grant, err := s.localGrant(id, a.now())
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

func (a *Agent) syncOfflineGrants(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, admissionControlTimeout)
	defer cancel()
	a.offlineMu.Lock()
	defer a.offlineMu.Unlock()
	states, err := a.ledger.offlineStates()
	if err != nil {
		return err
	}
	// Only an upgraded core proves new connections use protocol billing.
	// Probe once per sweep, and only if a legacy grant still needs migration.
	migrate := a.hasIdleLegacyGrant(states) && a.coreUsesProtocolBilling(ctx)
	for i := range states {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s := &states[i]
		a.cacheMu.RLock()
		allowed := a.cache.PortalEnabled && a.cache.Retirement == nil
		a.cacheMu.RUnlock()
		if !allowed {
			s.Denied = true
		}
		if migrate {
			if err = a.retireIdleLegacyGrant(ctx, s); err != nil {
				return err
			}
		}
		_ = a.syncOfflineState(ctx, s, false)
	}
	return nil
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

// A wall-clock expiry alone is not proof of closure: HY2 enforces monotonic
// deadlines. Reconcile a stable core generation and an empty online snapshot,
// after the last possible short lease has elapsed on our monotonic clock.
// A confirmed core restart also proves the old transport is gone. Neither
// observation returns its unconfirmed delegated bytes to the available pool.
func (a *Agent) reconcileOfflineChildren(ctx context.Context, s *offlineState, generation string) {
	for id, child := range s.Children {
		if child.Closed || child.Ended {
			continue
		}
		ended := child.CoreGeneration != "" && child.CoreGeneration != generation
		issued, ok := a.offlineIssued[id]
		if !ok {
			issued = a.offlineBoot
		}
		if !ended && !issued.IsZero() && time.Since(issued) >= 125*time.Second {
			var online map[string]int
			probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			err := a.statsJSON(probe, http.MethodGet, "/online", nil, &online)
			after, generationErr := a.generation(probe)
			cancel()
			ended = err == nil && online != nil && online["subscription_"+s.Grant.SubscriptionID] == 0 && generationErr == nil && after == generation
		}
		if ended {
			child.Ended = true
			s.Children[id] = child
		}
	}
}

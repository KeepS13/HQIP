package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func (a *Agent) offlineAdmit(w http.ResponseWriter, r *http.Request, connection, password string, protocols ...string) {
	protocol := ""
	if len(protocols) > 0 {
		protocol = protocols[0]
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), admissionControlTimeout)
	defer cancel()
	a.offlineMu.Lock()
	defer a.offlineMu.Unlock()
	now, hash := a.now(), secure.Hash(password)
	requestHash := hash
	if protocol != "" {
		requestHash = protocol + ":" + hash
	}
	if a.offlineIssued == nil {
		a.offlineIssued = map[string]time.Time{}
		a.offlineBoot = time.Now()
	}
	coreGeneration, coreErr := a.generation(ctx)
	if coreErr != nil || coreGeneration == "" {
		http.Error(w, "core state unavailable", 503)
		return
	}
	states, err := a.ledger.offlineStates()
	if err != nil {
		http.Error(w, "offline ledger unavailable", 503)
		return
	}
	var selected *offlineState
	for i := range states {
		s := &states[i]
		if protocol != "" && s.Grant.CredentialHash == hash {
			if err = a.retireIdleLegacyGrant(ctx, s); err != nil {
				http.Error(w, "legacy authorization settlement pending", 503)
				return
			}
		}
		matchesProtocol := s.Grant.Billing == nil && protocol == "" || s.Grant.Billing != nil && s.Grant.Billing.Protocol == protocol
		if matchesProtocol && s.Grant.CredentialHash == hash && !s.Grant.Closed && !s.Denied && !s.Grant.Revoked && now.Before(s.Grant.OfflineUntil) {
			a.reconcileOfflineChildren(ctx, s, coreGeneration)
			if err = a.ledger.saveOffline(*s); err != nil {
				http.Error(w, "offline reconciliation persistence failed", 503)
				return
			}
			available := true
			for id, child := range s.Children {
				if id != "off_"+connection && !child.Closed && !child.Ended {
					available = false
				}
			}
			if available {
				selected = s
				break
			}
		}
	}
	if selected == nil {
		if !a.offlineAllowed() {
			http.Error(w, "offline grant issuance disabled", 403)
			return
		}
		// Persist a credential-scoped request key before dispatch. Retrying a lost
		// grant response must not reserve a second slot for another QUIC dial.
		a.ledger.mu.Lock()
		_, err = a.ledger.db.Exec("INSERT OR IGNORE INTO offline_requests(hash,request_id) VALUES(?,?)", requestHash, connection)
		var requestID string
		if err == nil {
			err = a.ledger.db.QueryRow("SELECT request_id FROM offline_requests WHERE hash=?", requestHash).Scan(&requestID)
		}
		a.ledger.mu.Unlock()
		if err != nil {
			http.Error(w, "offline request persistence failed", 503)
			return
		}
		body := map[string]string{"connectionId": requestID, "password": password}
		if protocol != "" {
			body["billingProtocol"] = protocol
			body["billingCapability"] = "traffic-billing-v1"
			if a.nodeQuotaEnabled() {
				body["quotaCapability"] = model.NodeQuotaCapability
			}
		}
		status, data, requestErr := a.requestAdmission(ctx, "/api/v1/agents/offline-grants", body)
		if requestErr != nil || status != 200 {
			if !offlineRetryable(status, requestErr) {
				a.ledger.mu.Lock()
				_, _ = a.ledger.db.Exec("DELETE FROM offline_requests WHERE hash=?", requestHash)
				a.ledger.mu.Unlock()
			}
			_ = writeAdmissionResponse(w, status, data, requestErr)
			return
		}
		var g model.OfflineGrant
		if json.Unmarshal(data, &g) != nil || !validOfflineGrant(g, a.cfg.NodeID, hash, now) || g.ReportVersion != 0 || g.UploadBytes != 0 || g.DownloadBytes != 0 || g.EmergencyUsedBytes != 0 {
			http.Error(w, "invalid offline grant", 403)
			return
		}
		s := offlineState{Grant: g, Children: map[string]offlineChild{}, Online: true, LastWall: now, NextVersion: g.ReportVersion, RetryAt: now.Add(5 * time.Second)}
		if err = s.rememberBilling(); err != nil {
			http.Error(w, "invalid billing grant", 503)
			return
		}
		if err = a.ledger.saveOffline(s); err != nil {
			http.Error(w, "offline grant persistence failed", 503)
			return
		}
		a.ledger.mu.Lock()
		_, err = a.ledger.db.Exec("DELETE FROM offline_requests WHERE hash=?", requestHash)
		a.ledger.mu.Unlock()
		if err != nil {
			http.Error(w, "offline request persistence failed", 503)
			return
		}
		selected = &s
	} else if err = a.syncOfflineState(ctx, selected, false); err != nil {
		status := 503
		if errors.Is(err, errOfflineDenied) {
			status = 403
		}
		http.Error(w, "offline authorization unavailable", status)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	id := "off_" + connection
	grant, err := a.localOfflineGrant(selected, id, a.now())
	if err == nil {
		child := selected.Children[id]
		child.CoreGeneration = coreGeneration
		selected.Children[id] = child
		a.offlineIssued[id] = time.Now()
	}
	if saveErr := a.ledger.saveOffline(*selected); saveErr != nil {
		http.Error(w, "offline grant persistence failed", 503)
		return
	}
	if err != nil {
		http.Error(w, "offline authorization ended or slot occupied", 403)
		return
	}
	data, _ := json.Marshal(grant)
	// An uncertain delivery retains its short lease and delegated bytes. Never
	// manufacture a zero final that could overlap a successfully delivered grant.
	_ = writeAdmissionResponse(w, 200, data, nil)
}

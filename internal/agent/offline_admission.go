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

func (a *Agent) offlineAdmit(w http.ResponseWriter, r *http.Request, connection, password, protocol, clientIP string, transport ...string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), admissionControlTimeout)
	defer cancel()
	a.offlineMu.Lock()
	defer a.offlineMu.Unlock()
	now, hash := a.now(), secure.Hash(password)
	requestHash := protocol + ":" + hash + ":" + clientIP
	if a.offlineIssued == nil {
		a.offlineIssued = map[string]time.Time{}
		a.offlineBoot = time.Now()
	}
	coreGeneration, coreErr := a.forwardingGeneration(ctx, transport...)
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
		if matchesProtocol && s.Grant.CredentialHash == hash && s.Grant.ClientIP == clientIP && !s.Grant.Closed && !s.Denied && !s.Grant.Revoked && now.Before(s.Grant.OfflineUntil) {
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
		// Persist an IP/protocol/credential request key before dispatch. A lost
		// response must not issue duplicate accounting parents, and a different
		// IP must never inherit this request's approved identity.
		requestID, fresh, requestErr := a.ledger.trackOfflineRequest(requestHash, connection)
		if requestErr != nil {
			http.Error(w, "offline request persistence failed", 503)
			return
		}
		body := map[string]string{"connectionId": requestID, "password": password, "clientIP": clientIP, "billingProtocol": protocol, "billingCapability": "traffic-billing-v1", "quotaCapability": model.NodeQuotaCapability}
		status, data, requestErr := a.requestAdmission(ctx, "/api/v1/agents/offline-grants", body)
		if requestErr != nil || status != 200 {
			// A definitive first rejection did not reserve a slot. Once a prior
			// outcome is unknown, a later rejection (e.g. rotated credentials)
			// cannot erase its recovery key. Malformed/truncated 200s are unknown.
			if fresh && requestErr == nil && status >= 400 && status < 500 && !offlineRetryable(status, nil) {
				_ = a.ledger.forgetOfflineRequest(offlineRequest{Hash: requestHash, ID: requestID})
			}
			_ = writeAdmissionResponse(w, status, data, requestErr)
			return
		}
		var g model.OfflineGrant
		if json.Unmarshal(data, &g) != nil {
			http.Error(w, "invalid offline grant", 403)
			return
		}
		// A crash can leave both a saved parent and its request key. If that
		// parent is busy/denied it was not selected above; replay must never
		// overwrite its children with a new, empty accounting state.
		for _, existing := range states {
			if existing.Grant.ID == g.ID {
				if sameOfflineIdentity(existing.Grant, g) {
					_ = a.ledger.forgetOfflineRequest(offlineRequest{Hash: requestHash, ID: requestID})
				}
				http.Error(w, "previous offline admission already tracked", 503)
				return
			}
		}
		s, initialErr := initialOfflineState(g, a.cfg.NodeID, hash, protocol, clientIP, a.now())
		if initialErr != nil {
			http.Error(w, "invalid billing grant", 503)
			return
		}
		if err = a.ledger.saveOffline(s); err != nil {
			http.Error(w, "offline grant persistence failed", 503)
			return
		}
		err = a.ledger.forgetOfflineRequest(offlineRequest{Hash: requestHash, ID: requestID})
		if err != nil {
			http.Error(w, "offline request persistence failed", 503)
			return
		}
		selected = &s
	} else if err = a.prepareOfflineSlot(ctx, selected); err != nil {
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

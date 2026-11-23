package agent

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/KeepS13/HQIP/internal/secure"
)

// Check-only authentication has no ledger writes and no forwarding authority.
// During an outage only an existing, still-held same-IP/protocol authorization
// may establish the transport; actual use still passes through offlineAdmit.
func (a *Agent) checkAdmissionIdentity(w http.ResponseWriter, r *http.Request, password, protocol, ip string) {
	ctx, cancel := context.WithTimeout(r.Context(), admissionControlTimeout)
	defer cancel()
	status, data, err := a.requestAdmission(ctx, "/api/v1/agents/admission/check", map[string]string{"password": password})
	if offlineRetryable(status, err) {
		a.offlineMu.Lock()
		states, readErr := a.ledger.offlineStates()
		a.offlineMu.Unlock()
		if readErr == nil {
			hash := secure.Hash(password)
			for _, s := range states {
				g := s.Grant
				if !s.Denied && !g.SlotReleased && g.ClientIP == ip && g.Billing != nil && g.Billing.Protocol == protocol && validOfflineGrant(g, a.cfg.NodeID, hash, a.now()) {
					data, _ = json.Marshal(map[string]any{"authenticated": true, "accountingId": "subscription_" + g.SubscriptionID})
					status, err = http.StatusOK, nil
					break
				}
			}
		}
	}
	_ = writeAdmissionResponse(w, status, data, err)
}

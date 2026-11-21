package agent

import (
	"context"
	"net/http"
	"time"
)

// idleLegacyGrant requires explicit finals for every child. An expired or
// Ended child still owns uncertain bytes and must never release its parent.
func idleLegacyGrant(s *offlineState) bool {
	if s.Grant.Billing != nil || s.Grant.Closed {
		return false
	}
	for _, child := range s.Children {
		if !child.Closed {
			return false
		}
	}
	return true
}

func (a *Agent) hasIdleLegacyGrant(states []offlineState) bool {
	for i := range states {
		if idleLegacyGrant(&states[i]) {
			return true
		}
	}
	return false
}

func (a *Agent) coreUsesProtocolBilling(ctx context.Context) bool {
	probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var capabilities struct {
		Admission             bool `json:"admission"`
		Version               int  `json:"version"`
		TrafficBillingVersion int  `json:"trafficBillingVersion"`
	}
	return a.statsJSON(probe, http.MethodGet, "/hqip/capabilities", nil, &capabilities) == nil &&
		capabilities.Admission && capabilities.Version == 1 && capabilities.TrafficBillingVersion == 1
}

// retireIdleLegacyGrant is called with offlineMu held and upgrade evidence.
// Persist denial before sending the final so no reconnect can reuse a released
// reservation. A previously pending report is replayed unchanged first; only
// its acknowledgement permits the next version carrying the explicit final.
func (a *Agent) retireIdleLegacyGrant(ctx context.Context, s *offlineState) error {
	if !idleLegacyGrant(s) {
		return nil
	}
	s.Denied = true
	if err := a.ledger.saveOffline(*s); err != nil {
		return err
	}
	if err := a.syncOfflineState(ctx, s, true); err != nil {
		return err
	}
	if !s.Grant.Closed && s.Pending == nil {
		return a.syncOfflineState(ctx, s, true)
	}
	return nil
}

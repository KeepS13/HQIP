package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/apernet/hysteria/core/v2/billing"
)

// Old controllers require explicit finals. Node quota controllers can retire
// a proven-ended child while retaining its entire uncertain delegated budget.
func idleLegacyGrant(s *offlineState, nodeQuota ...bool) bool {
	migrateQuota := len(nodeQuota) == 1 && nodeQuota[0]
	if s.Grant.Closed || s.Grant.NodeQuota != nil || s.Grant.Billing != nil && !migrateQuota {
		return false
	}
	for _, child := range s.Children {
		if !child.Closed && !(migrateQuota && child.Ended) {
			return false
		}
	}
	return true
}

// retainedLegacyUnits never guesses final usage. Closed children contribute
// their exact usage; ended children retain every byte the core could consume.
func retainedLegacyUnits(s *offlineState) (*int64, error) {
	if !s.Denied || !idleLegacyGrant(s, true) || s.Grant.NormalUnlimited {
		return nil, nil
	}
	var units int64
	for _, child := range s.Children {
		amount := child.Normal
		var err error
		if s.Grant.Billing == nil {
			amount, err = billing.Units(amount, 100)
			if err != nil {
				return nil, err
			}
		}
		units, err = billing.Add(units, amount)
		if err != nil {
			return nil, err
		}
	}
	return &units, nil
}

func (a *Agent) hasIdleLegacyGrant(states []offlineState) bool {
	for i := range states {
		if idleLegacyGrant(&states[i], a.nodeQuotaEnabled()) {
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
	if !idleLegacyGrant(s, a.nodeQuotaEnabled()) {
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

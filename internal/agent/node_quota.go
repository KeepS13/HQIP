package agent

import (
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func (a *Agent) nodeQuotaEnabled() bool {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	return a.cache.NodeQuotaVersion == 1
}

func validNodeQuotaSnapshot(q *model.NodeQuotaSnapshot) bool {
	return q != nil && q.Revision > 0 && q.UsedUnits >= 0 && q.NodeUsedUnits >= 0 && q.NodeUsedUnits <= q.UsedUnits && q.LegacyReservedUnits >= 0 && (q.QuotaUnits == nil || *q.QuotaUnits >= 0)
}

// localOfflineGrant owns the shared subscription budget under offlineMu. All
// protocols, sessions and parents on this node compete for the same remaining
// units. Already delegated bytes remain unavailable until an explicit final;
// offline or restarted cores cannot free an unknown outstanding allocation.
func (a *Agent) localOfflineGrant(s *offlineState, id string, now time.Time) (model.ConnectionLease, error) {
	if s.Grant.NodeQuota == nil {
		return s.localGrant(id, now)
	}
	states, err := a.ledger.offlineStates()
	if err != nil {
		return model.ConnectionLease{}, err
	}
	// The caller may hold a freshly acknowledged state not yet present in the
	// read snapshot. Replace, never append a duplicate of that parent.
	found := false
	for i := range states {
		if states[i].Grant.ID == s.Grant.ID {
			states[i] = *s
			found = true
		}
	}
	if !found {
		states = append(states, *s)
	}
	available, err := nodeQuotaAvailable(states, s.Grant.SubscriptionID, s.Grant.NodeQuota.PeriodID)
	if err != nil {
		return model.ConnectionLease{}, err
	}
	s.QuotaAvailableUnits = available
	return s.localGrant(id, now)
}

func nodeQuotaAvailable(states []offlineState, subscription, period string) (int64, error) {
	// Legacy grants have no period ID and belong to the original period.
	// Outstanding allocations from a closed period never consume a new one.
	if period == "" {
		period = subscription
	}
	var latest *model.NodeQuotaSnapshot
	var used, delegated int64
	for _, s := range states {
		q := s.Grant.NodeQuota
		if s.Grant.SubscriptionID != subscription || q == nil {
			continue
		}
		grantPeriod := q.PeriodID
		if grantPeriod == "" {
			grantPeriod = subscription
		}
		if grantPeriod != period {
			continue
		}
		if !validNodeQuotaSnapshot(q) {
			return 0, errOfflineDenied
		}
		if latest == nil || q.Revision > latest.Revision {
			latest = q
		}
		for _, child := range s.Children {
			var err error
			used, err = billing.Add(used, child.NormalUsed)
			if err != nil {
				return 0, err
			}
			delegated, err = billing.Add(delegated, child.Normal)
			if err != nil {
				return 0, err
			}
		}
	}
	// A missing ledger cannot be reconstructed from an acknowledgement. This
	// also detects counter loss across restart before issuing any new bytes.
	if latest == nil || used < latest.NodeUsedUnits || delegated < used {
		return 0, errOfflineDenied
	}
	if latest.QuotaUnits == nil {
		return billing.MaxRawBytes, nil
	}
	unreported := used - latest.NodeUsedUnits
	remaining := *latest.QuotaUnits - latest.UsedUnits
	if remaining <= 0 || unreported >= remaining {
		return 0, errOfflineDenied
	}
	remaining -= unreported
	if latest.LegacyReservedUnits >= remaining {
		return 0, errOfflineDenied
	}
	remaining -= latest.LegacyReservedUnits
	return max(int64(0), remaining-(delegated-used)), nil
}

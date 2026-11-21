package agent

import (
	"database/sql"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
	"time"
)

func (state *offlineState) rememberBilling() error {
	grant := state.Grant.Billing
	if grant == nil {
		return nil
	}
	if !grant.Policy.Valid() || grant.BudgetUnits < 0 || grant.EmergencyBytes != state.Grant.EmergencyBudgetBytes {
		return errOfflineDenied
	}
	if state.BillingPolicies == nil {
		state.BillingPolicies = map[int64]billing.Policy{}
	}
	if old, ok := state.BillingPolicies[grant.Version]; ok && old != grant.Policy {
		return errOfflineDenied
	}
	state.BillingPolicies[grant.Version] = grant.Policy
	if state.BillingReported == nil {
		state.BillingReported = map[int64]billing.Segment{}
	}
	for _, segment := range grant.Acknowledged {
		state.BillingReported[segment.Version] = segment
	}
	return nil
}

func (state *offlineState) updateBillingChild(id string, usage model.LeaseUsage) error {
	child, ok := state.Children[id]
	if !ok {
		return sql.ErrNoRows
	}
	if usage.UploadBytes < child.Upload || usage.DownloadBytes < child.Download || usage.AppliedRateVersion < child.AppliedRate || usage.AppliedRateVersion > state.Grant.RateVersion || child.Closed && (!usage.Closed || usage.UploadBytes != child.Upload || usage.DownloadBytes != child.Download) {
		return errLeaseCounterRegression
	}
	if child.Billing == nil {
		child.Billing = map[int64]billing.Segment{}
	}
	seen := map[int64]bool{}
	for _, segment := range usage.Billing {
		_, ok := state.BillingPolicies[segment.Version]
		previous := child.Billing[segment.Version]
		if !ok || seen[segment.Version] || segment.Validate() != nil || segment.Upload < previous.Upload || segment.Download < previous.Download || segment.EmergencyUpload < previous.EmergencyUpload || segment.EmergencyDownload < previous.EmergencyDownload || (!previous.AppliedAt.IsZero() && !segment.AppliedAt.Equal(previous.AppliedAt)) {
			return errLeaseCounterRegression
		}
		seen[segment.Version] = true
		child.Billing[segment.Version] = segment
	}
	var upload, download, normal, emergency int64
	for version, segment := range child.Billing {
		var err error
		if upload, err = billing.Add(upload, segment.Upload); err != nil {
			return err
		}
		if download, err = billing.Add(download, segment.Download); err != nil {
			return err
		}
		if emergency, err = billing.Add(emergency, segment.EmergencyUpload+segment.EmergencyDownload); err != nil {
			return err
		}
		units, err := billing.Units(segment.Upload-segment.EmergencyUpload+segment.Download-segment.EmergencyDownload, state.BillingPolicies[version].Multiplier)
		if err != nil {
			return err
		}
		if normal, err = billing.Add(normal, units); err != nil {
			return err
		}
	}
	if upload != usage.UploadBytes || download != usage.DownloadBytes || normal > child.Normal || emergency > child.Emergency {
		return errLeaseCounterRegression
	}
	child.Upload, child.Download, child.NormalUsed, child.EmergencyUsed = upload, download, normal, emergency
	child.AppliedRate = usage.AppliedRateVersion
	child.BillingAcknowledged = append([]billing.Segment(nil), usage.Billing...)
	if usage.Closed {
		child.Closed = true
		child.Normal = normal
		child.Emergency = emergency
	}
	state.Children[id] = child
	return nil
}

func (state *offlineState) localBillingGrant(id string, now time.Time) (model.ConnectionLease, error) {
	var empty model.ConnectionLease
	if err := state.observe(now); err != nil {
		return empty, err
	}
	if err := state.rememberBilling(); err != nil {
		return empty, err
	}
	child, exists := state.Children[id]
	if !exists {
		if len(state.Children) >= 4096 {
			return empty, errOfflineDenied
		}
		for _, other := range state.Children {
			if !other.Closed && !other.Ended {
				return empty, errOfflineDenied
			}
		}
		child = offlineChild{ID: id, Billing: map[int64]billing.Segment{}}
	} else if child.Closed || child.Ended || !now.Before(child.Until) {
		return empty, errOfflineDenied
	}
	var normal, emergency int64
	for _, other := range state.Children {
		var err error
		if normal, err = billing.Add(normal, other.Normal); err != nil {
			return empty, err
		}
		emergency += other.Emergency
	}
	policy := state.Grant.Billing.Policy
	window, err := billing.Units(model.OfflineChunkBytes, policy.Multiplier)
	if err != nil {
		return empty, err
	}
	need := max(int64(0), window-(child.Normal-child.NormalUsed))
	available := max(int64(0), state.Grant.Billing.BudgetUnits-normal)
	if state.Grant.NormalUnlimited {
		available = need
	}
	if state.Grant.NodeQuota != nil {
		available = min(available, state.QuotaAvailableUnits)
	}
	if child.Emergency == 0 {
		add := min(need, available)
		child.Normal += add
		need -= add
	}
	if !state.Online && need > 0 {
		child.Emergency += min(model.OfflineChunkBytes, max(int64(0), state.Grant.EmergencyBudgetBytes-emergency))
	}
	if (child.Normal-child.NormalUsed)/policy.Multiplier+child.Emergency-child.EmergencyUsed <= 0 {
		return empty, errOfflineDenied
	}
	child.Until = now.Add(120 * time.Second)
	if child.Until.After(state.Grant.OfflineUntil) {
		child.Until = state.Grant.OfflineUntil
	}
	state.Children[id] = child
	grant := &billing.Grant{Policy: policy, BudgetUnits: child.Normal, EmergencyBytes: child.Emergency, Acknowledged: child.BillingAcknowledged}
	return model.ConnectionLease{ID: id, LeaseID: id, UserID: state.Grant.UserID, SubscriptionID: state.Grant.SubscriptionID, NodeID: state.Grant.NodeID, AccountingID: "subscription_" + state.Grant.SubscriptionID, ExpiresAt: child.Until, BudgetBytes: billing.MaxRawBytes, ConsumedBytes: child.Upload + child.Download, UploadBytes: child.Upload, DownloadBytes: child.Download, Revision: state.Grant.Revision, RateVersion: state.Grant.RateVersion, BandwidthBytesPerSecond: state.Grant.BandwidthBytesPerSecond, RatePending: state.Grant.RatePending, Billing: grant}, nil
}

package agent

import (
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// offlineReceiptValid validates a response to the exact durable pending report.
// Control owns entitlement deadlines, including administrator shortening. A
// delayed receipt is validated at its authorization time, not its arrival time:
// expiry cannot block accounting acknowledgement and subsequent slot release.
// This does not grant forwarding permission; observe/localOfflineGrant still
// enforce the current clock, revocation, denial and central slot ownership.
func offlineReceiptValid(s *offlineState, g model.OfflineGrant, u model.OfflineUsage, now time.Time) bool {
	old := s.Grant
	if g.ID != old.ID || g.NodeID != old.NodeID || g.CredentialHash != old.CredentialHash ||
		g.UserID != old.UserID || g.SubscriptionID != old.SubscriptionID || g.SessionID != old.SessionID {
		return false
	}
	if g.AuthorizedAt.Before(old.AuthorizedAt) || g.AuthorizedAt.After(now.Add(time.Minute)) ||
		g.ReportVersion != u.Version || g.UploadBytes != u.UploadBytes || g.DownloadBytes != u.DownloadBytes ||
		g.EmergencyUsedBytes != u.EmergencyUsedBytes || g.Closed != u.Closed || g.RateVersion < old.RateVersion {
		return false
	}
	if (g.NormalBudgetBytes < old.NormalBudgetBytes && u.RetainedNormalUnits == nil) ||
		g.EmergencyBudgetBytes != old.EmergencyBudgetBytes || g.NormalUnlimited != old.NormalUnlimited {
		return false
	}
	if !g.Closed && !g.Revoked && !validOfflineGrant(g, old.NodeID, old.CredentialHash, g.AuthorizedAt) {
		return false
	}
	if !slotAcknowledgementValid(s, g, u) || (old.NodeQuota != nil) != (g.NodeQuota != nil) {
		return false
	}
	if g.NodeQuota != nil && (!validNodeQuotaSnapshot(g.NodeQuota) || g.NodeQuota.Revision < old.NodeQuota.Revision) {
		return false
	}
	if old.Billing != nil && (g.Billing == nil || g.Billing.Protocol != old.Billing.Protocol ||
		g.Billing.Version < old.Billing.Version || (g.Billing.BudgetUnits < old.Billing.BudgetUnits && u.RetainedNormalUnits == nil)) {
		return false
	}
	return u.RetainedNormalUnits == nil || (g.Revoked && g.NormalBudgetBytes == (*u.RetainedNormalUnits+99)/100 &&
		(g.Billing == nil || g.Billing.BudgetUnits == *u.RetainedNormalUnits))
}

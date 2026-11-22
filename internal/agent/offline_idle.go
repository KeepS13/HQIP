package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/KeepS13/HQIP/internal/model"
)

func offlineReportDigest(u model.OfflineUsage) string {
	// Versions and acknowledged commands/segments are consumed by the reply.
	// New commands and unacknowledged billing segments independently prevent
	// sleep below, even when the cumulative byte totals have not changed.
	u.Version, u.SlotAction, u.Billing = 0, "", nil
	data, err := json.Marshal(u)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *offlineState) idleReportConfirmed(u model.OfflineUsage) bool {
	return s.Pending == nil && s.AcknowledgedReport != "" &&
		(s.Grant.SlotReleased || s.Grant.Revoked) && idleOfflineSlot(s) &&
		!u.Active && !u.Closed && u.SlotAction == "" && len(u.Billing) == 0 &&
		s.AcknowledgedReport == offlineReportDigest(u)
}

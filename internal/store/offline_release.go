package store

import (
	"database/sql"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

// releaseUndelegatedOffline runs after usage settlement, under the same
// subscription transaction. The trusted Agent has durably denied this parent
// and retains every uncertain child's full allocation. Only unissued capacity
// returns to the subscription; this does not manufacture a final receipt.
func releaseUndelegatedOffline(tx *sql.Tx, g *model.OfflineGrant, state *leaseBilling, u model.OfflineUsage) error {
	if u.RetainedNormalUnits == nil {
		return nil
	}
	units := *u.RetainedNormalUnits
	if units < 0 || u.Active || g.NodeQuota != nil || g.NormalUnlimited {
		return ErrConflict
	}
	var release int64
	if state == nil {
		if units%100 != 0 {
			return ErrConflict
		}
		retained := units / 100
		if retained < u.UploadBytes+u.DownloadBytes-u.EmergencyUsedBytes || retained > g.NormalBudgetBytes {
			return ErrConflict
		}
		release = g.NormalBudgetBytes - retained
	} else {
		if units < state.NormalUsed || units > state.Grant.BudgetUnits {
			return ErrConflict
		}
		reserved := billing.Ceiling(units - state.NormalUsed)
		if reserved > state.Reserved {
			return ErrConflict
		}
		release = state.Reserved - reserved
		state.Reserved, state.Grant.BudgetUnits = reserved, units
	}
	if _, err := tx.Exec("UPDATE portal_usage SET reserved=reserved-? WHERE subscription_id=?", release, g.SubscriptionID); err != nil {
		return err
	}
	g.NormalBudgetBytes = billing.Ceiling(units)
	g.Revoked = true
	return nil
}

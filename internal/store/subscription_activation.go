package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// A row records manual provisioning for the lifetime of this subscription,
// including after activation. This keeps template pool edits from reallocating
// custom nodes. Only business state is stored; legacy subscriptions have no row.
const subscriptionProvisioningSchema = `
CREATE TABLE IF NOT EXISTS subscription_provisioning(subscription_id TEXT PRIMARY KEY REFERENCES portal_subscriptions(id),validity_days BIGINT NOT NULL CHECK(validity_days>=1 AND validity_days<=36500),activated_at BIGINT NOT NULL DEFAULT 0);
`

// Snapshot purchase mode/count before using live inventory. An originally empty
// pool always awaits an administrator, even if the template now has nodes. An
// automatic order with depleted inventory must still fail as out of stock.
func initialSubscriptionAllocation(tx *sql.Tx, user, key string, p model.SubscriptionTemplate, now time.Time) (int, []string, bool, error) {
	target, manual := subscriptionNodeTarget(p), len(p.NodeIDs) == 0
	if !strings.HasPrefix(key, "payment:") {
		if manual {
			return target, []string{}, true, nil
		}
		ids, err := allocateSubscriptionNodes(tx, user, p, now)
		return target, ids, false, err
	}
	orderID := strings.TrimPrefix(key, "payment:")
	var raw string
	if err := tx.QueryRow("SELECT template_snapshot FROM payment_orders WHERE id=?", orderID).Scan(&raw); err != nil {
		return 0, nil, false, err
	}
	var snapshot model.SubscriptionTemplate
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return 0, nil, false, err
	}
	normalizeSubscriptionTemplate(&snapshot)
	target = subscriptionNodeTarget(snapshot)
	if len(snapshot.NodeIDs) == 0 {
		return target, []string{}, true, nil
	}
	var stored int
	if err := tx.QueryRow("SELECT target_count FROM payment_node_reservations WHERE order_id=?", orderID).Scan(&stored); err == nil {
		target = stored
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, false, err
	}
	ids, err := reserveOrderNodes(tx, orderID, user, p, target, now)
	return target, ids, false, err
}

// Caller holds the allocation-user lock and has validated/replaced the full
// saved node count. Activation, first period and node replacement share that
// transaction. Replay or subsequent node edits never reset expiry or usage.
func activatePendingSubscription(tx *sql.Tx, current *model.UserSubscription, now time.Time) error {
	if !current.ActivationPending {
		return nil
	}
	if current.Status != "pending" {
		return ErrEntitlement
	}
	expires := now.Add(time.Duration(current.ValidityDays) * 24 * time.Hour)
	if _, err := tx.Exec("INSERT INTO subscription_periods(id,subscription_id,starts_at,ends_at) VALUES(?,?,?,?)", current.ID, current.ID, now.Unix(), expires.Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", expires.Unix(), current.ID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE subscription_provisioning SET activated_at=? WHERE subscription_id=? AND activated_at=0", now.Unix(), current.ID); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.activated',?)", now.Unix(), current.ID)
	return err
}

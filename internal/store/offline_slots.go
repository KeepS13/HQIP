package store

import (
	"database/sql"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// releaseOfflineSlot runs after accounting and before entitlement/rate reads
// under the subscription transaction. Only an explicit node transition may
// free a slot: a missing heartbeat is not proof an offline client disconnected.
func releaseOfflineSlot(tx *sql.Tx, g *model.OfflineGrant, state *leaseBilling, u model.OfflineUsage, now time.Time) error {
	if u.SlotAction == "" {
		return nil
	}
	if state == nil || state.NodeQuota == nil || u.Active || u.Closed && u.SlotAction != "release" || (u.SlotAction != "release" && u.SlotAction != "acquire") {
		return ErrConflict
	}
	if state.AuthorizationUntil.IsZero() {
		state.AuthorizationUntil = g.OfflineUntil
	}
	if _, err := tx.Exec("INSERT INTO metadata(key,value) VALUES('connection_slots_version','1') ON CONFLICT(key) DO NOTHING"); err != nil {
		return err
	}
	if u.SlotAction != "release" || state.SlotReleased {
		return nil
	}
	state.SlotReleased, g.SlotReleased = true, true
	if _, err := tx.Exec("UPDATE portal_leases SET expires=?,bandwidth=0 WHERE id=?", now.Unix(), g.ID); err != nil {
		return err
	}
	// The node fenced every child before releasing, so no old rate ceiling
	// remains live. Reset the requested rate as well so reacquisition reserves
	// a new ceiling before forwarding; an unchanged old rate would skip that
	// reservation. Unknown final counters remain in the metering ledger.
	_, err := tx.Exec("UPDATE portal_rates SET ceiling=0,pending=1,version=version+1 WHERE lease_id=?", g.ID)
	return err
}

// Reacquisition never discards accounting when the subscription is full. The
// acknowledged grant remains released, allowing a later idempotent retry.
func acquireOfflineSlot(g *model.OfflineGrant, state *leaseBilling, u model.OfflineUsage, e *model.UserSubscription) {
	if state == nil || state.AuthorizationUntil.IsZero() {
		return
	}
	state.AuthorizationUntil = g.OfflineUntil
	if u.SlotAction == "acquire" && state.SlotReleased && !g.Revoked && e.Occupied < e.MaxConnections {
		state.SlotReleased = false
	}
	g.SlotReleased = state.SlotReleased
}

func offlineSlotExpiry(g model.OfflineGrant, now time.Time) int64 {
	if g.SlotReleased {
		return now.Unix()
	}
	return g.OfflineUntil.Unix()
}

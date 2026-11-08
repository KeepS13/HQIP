package store

import (
	"database/sql"
	"github.com/KeepS13/HQIP/internal/model"
	"time"
)

// ceiling represents the largest rate a node may still apply. A decrease only
// releases its capacity after the core acknowledges that exact rate version.
// A new connection starts paused; all increases reserve capacity immediately.
// -1 denotes a previously granted unlimited rate awaiting a finite-rate ACK.
func rebalancePortalRates(tx *sql.Tx, subscription string, e *model.UserSubscription, now time.Time) error {
	type member struct {
		id                     string
		rate, ceiling, version int64
		pending, revoked       bool
		node                   string
	}
	// Correlated primary-key reads avoid a hash join that scans every rate row
	// and takes Serializable predicate locks for unrelated subscriptions.
	rows, err := tx.Query("SELECT l.id,l.bandwidth,(SELECT ceiling FROM portal_rates WHERE lease_id=l.id),(SELECT version FROM portal_rates WHERE lease_id=l.id),(SELECT pending FROM portal_rates WHERE lease_id=l.id),l.revoked,l.node_id FROM portal_leases l WHERE l.subscription_id=? AND l.closed=0 AND l.expires>? ORDER BY l.created_at,l.rowid", subscription, now.Unix())
	if err != nil {
		return err
	}
	members := []member{}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.id, &m.rate, &m.ceiling, &m.version, &m.pending, &m.revoked, &m.node); err != nil {
			rows.Close()
			return err
		}
		members = append(members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	count := 0
	eligible := map[string]bool{}
	for _, m := range members {
		if !m.revoked && entitlementAllows(e, m.node) && count < e.MaxConnections {
			eligible[m.id] = true
			count++
		}
	}
	total := leaseRate(e)
	target := int64(0)
	if count > 0 {
		target = total / int64(count)
	}
	update := func(m *member, rate int64, pending bool, ceiling int64) error {
		if m.rate == rate && m.pending == pending {
			return nil
		}
		m.version++
		m.rate = rate
		m.pending = pending
		m.ceiling = ceiling
		if _, err := tx.Exec("UPDATE portal_leases SET bandwidth=? WHERE id=?", rate, m.id); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE portal_rates SET version=?,pending=?,ceiling=? WHERE lease_id=?", m.version, pending, ceiling, m.id)
		return err
	}
	// First request all necessary reductions. Keep their previous ceilings until
	// acknowledged, so delayed nodes cannot overlap with newly released rates.
	for i := range members {
		m := &members[i]
		if !eligible[m.id] {
			if err = update(m, 0, true, m.ceiling); err != nil {
				return err
			}
			continue
		}
		if e.BandwidthMbps == nil {
			if err = update(m, 0, false, -1); err != nil {
				return err
			}
			continue
		}
		if !m.pending && (m.rate == 0 || m.rate > target) {
			if err = update(m, target, target == 0, m.ceiling); err != nil {
				return err
			}
		}
	}
	if e.BandwidthMbps == nil {
		return nil
	}
	for i := range members {
		m := &members[i]
		if !eligible[m.id] || (!m.pending && m.rate >= target) {
			continue
		}
		available := total
		blocked := false
		for j := range members {
			if j == i {
				continue
			}
			if members[j].ceiling < 0 {
				blocked = true
				break
			}
			available -= members[j].ceiling
		}
		if blocked || available <= 0 {
			continue
		}
		rate := target
		if rate > available {
			rate = available
		}
		if rate <= 0 {
			continue
		}
		ceiling := m.ceiling
		if ceiling < rate {
			ceiling = rate
		}
		if err = update(m, rate, false, ceiling); err != nil {
			return err
		}
	}
	return nil
}

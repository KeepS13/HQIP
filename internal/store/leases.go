package store

import (
	"database/sql"
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
	"time"
)

const leaseSelect = `SELECT id,node_id,user_id,session_id,subscription_id,revision,created_at,expires,bandwidth,budget,upload,download,closed,revoked,COALESCE((SELECT version FROM portal_rates WHERE lease_id=id),1),COALESCE((SELECT pending FROM portal_rates WHERE lease_id=id),1),client_ip FROM portal_leases `

type storedLease struct {
	model.ConnectionLease
	revoked bool
}
type rowScanner interface{ Scan(...any) error }

func scanLease(row rowScanner) (storedLease, error) {
	var l storedLease
	var created, expires int64
	err := row.Scan(&l.ID, &l.NodeID, &l.UserID, &l.SessionID, &l.SubscriptionID, &l.Revision, &created, &expires, &l.BandwidthBytesPerSecond, &l.BudgetBytes, &l.UploadBytes, &l.DownloadBytes, &l.Closed, &l.revoked, &l.RateVersion, &l.RatePending, &l.ClientIP)
	l.LeaseID = l.ID
	l.AccountingID = "subscription_" + l.SubscriptionID
	l.CreatedAt = time.Unix(created, 0).UTC()
	l.ExpiresAt = time.Unix(expires, 0).UTC()
	l.ConsumedBytes = l.UploadBytes + l.DownloadBytes
	l.State = "active"
	if l.Closed {
		l.State = "closed"
	} else if l.revoked {
		l.State = "closing"
	}
	return l, err
}
func entitlementAllows(e *model.UserSubscription, node string) bool {
	if e == nil || e.Status != "active" {
		return false
	}
	for _, n := range e.NodeIDs {
		if n == node {
			return true
		}
	}
	return false
}
func leaseRate(e *model.UserSubscription) int64 {
	if e.BandwidthMbps == nil {
		return 0
	}
	rate := int64(*e.BandwidthMbps * 1e6 / 8)
	if rate < 1 {
		return 1
	}
	return rate
}
func leaseWindow(rate int64) int64 {
	if rate == 0 {
		return 16 << 20
	}
	n := rate * 10
	if n < 1<<20 {
		n = 1 << 20
	}
	if n > 16<<20 {
		n = 16 << 20
	}
	return n
}
func leaseExpiry(now time.Time, e *model.UserSubscription) time.Time {
	end := now.Add(120 * time.Second)
	if end.After(e.ExpiresAt) {
		end = e.ExpiresAt
	}
	if !e.CurrentPeriodEndsAt.IsZero() && end.After(e.CurrentPeriodEndsAt) {
		end = e.CurrentPeriodEndsAt
	}
	return end.UTC()
}
func activeCredentialSession(tx *sql.Tx, user, subscription, scope string, now time.Time) (bool, error) {
	var n int
	var err error
	if scope == "subscription" {
		err = tx.QueryRow("SELECT COUNT(*) FROM portal_subscriptions WHERE id=? AND user_id=? AND subscription_hash!='' AND revoked=0", subscription, user).Scan(&n)
	} else {
		err = tx.QueryRow("SELECT COUNT(*) FROM portal_sessions WHERE id=? AND user_id=? AND kind='native' AND revoked=0 AND expires>? AND selected_subscription_id=?", scope, user, now.Unix(), subscription).Scan(&n)
	}
	return n == 1, err
}

// AcquireProtocolLease admits a short online lease. The trusted node must
// provide its observed peer IP; account/download addresses are not substitutes.
func (s *Store) AcquireProtocolLease(node, connection, password, protocol, clientIP string) (model.ConnectionLease, error) {
	if protocol != "hysteria2" && protocol != "vless" {
		return model.ConnectionLease{}, ErrConflict
	}
	ip, err := model.CanonicalClientIP(clientIP)
	if err != nil {
		return model.ConnectionLease{}, ErrUnauthorized
	}
	return s.acquireLease(node, connection, password, protocol, ip, false)
}

func reconcileSubscriptionLeases(tx *sql.Tx, e *model.UserSubscription, now time.Time) error {
	rows, err := tx.Query("SELECT l.id,l.node_id,l.client_ip,"+onlineProtocolSQL+" FROM portal_leases l WHERE l.subscription_id=? AND l.closed=0 AND l.revoked=0 AND l.expires>? ORDER BY l.created_at,l.rowid", e.ID, now.Unix())
	if err != nil {
		return err
	}
	type record struct{ id, node, ip, protocol string }
	leases := []record{}
	for rows.Next() {
		var r record
		if err = rows.Scan(&r.id, &r.node, &r.ip, &r.protocol); err != nil {
			rows.Close()
			return err
		}
		leases = append(leases, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	retained := map[string]bool{}
	for _, l := range leases {
		if !entitlementAllows(e, l.node) || e.QuotaBytes != nil && e.UsedBytes+e.ReservedBytes > *e.QuotaBytes || !keepOnlineSlot(retained, l.protocol, l.ip, l.id, e.MaxConnections) {
			if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE id=?", l.id); err != nil {
				return err
			}
		}
	}
	return rebalancePortalRates(tx, e.ID, e, now)
}
func (s *Store) PortalConnections(user, session string, subscriptions ...string) ([]model.ConnectionLease, error) {
	query := leaseSelect + "WHERE user_id=?"
	args := []any{user}
	if session != "" {
		query += " AND session_id=?"
		args = append(args, session)
	}
	if len(subscriptions) > 0 && subscriptions[0] != "" {
		query += " AND subscription_id=?"
		args = append(args, subscriptions[0])
	}
	query += " ORDER BY created_at DESC LIMIT 200"
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionLease{}
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		if !l.Closed && !l.ExpiresAt.After(s.Now()) {
			l.State = "expired"
		}
		out = append(out, l.ConnectionLease)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		var last int64
		err = s.DB.QueryRow("SELECT last_active FROM portal_offline_grants WHERE lease_id=?", out[i].ID).Scan(&last)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[i].OfflineReservation = true
		if last != 0 {
			stamp := time.Unix(last, 0).UTC()
			out[i].LastForwardingReport = &stamp
		}
		if out[i].State == "active" && (last == 0 || last < s.Now().Add(-120*time.Second).Unix()) {
			out[i].State = "reserved"
		}
	}
	return out, nil
}
func (s *Store) RevokePortalConnection(user, id, session string, subscriptions ...string) error {
	query := "UPDATE portal_leases SET revoked=1 WHERE user_id=?"
	args := []any{user}
	if id != "" {
		query += " AND id=?"
		args = append(args, id)
	}
	if session != "" {
		query += " AND session_id=?"
		args = append(args, session)
	}
	if len(subscriptions) > 0 && subscriptions[0] != "" {
		query += " AND subscription_id=?"
		args = append(args, subscriptions[0])
	}
	r, err := s.DB.Exec(query, args...)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 && id != "" {
		return ErrNotFound
	}
	return nil
}

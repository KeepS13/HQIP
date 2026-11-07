package store

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

const leaseSelect = `SELECT id,node_id,user_id,session_id,subscription_id,revision,created_at,expires,bandwidth,budget,upload,download,closed,revoked,COALESCE((SELECT version FROM portal_rates WHERE lease_id=id),1),COALESCE((SELECT pending FROM portal_rates WHERE lease_id=id),1) FROM portal_leases `

type storedLease struct {
	model.ConnectionLease
	revoked bool
}
type rowScanner interface{ Scan(...any) error }

func scanLease(row rowScanner) (storedLease, error) {
	var l storedLease
	var created, expires int64
	err := row.Scan(&l.ID, &l.NodeID, &l.UserID, &l.SessionID, &l.SubscriptionID, &l.Revision, &created, &expires, &l.BandwidthBytesPerSecond, &l.BudgetBytes, &l.UploadBytes, &l.DownloadBytes, &l.Closed, &l.revoked, &l.RateVersion, &l.RatePending)
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
	return end.UTC()
}
func activeCredentialSession(tx *sql.Tx, user, subscription, scope string, now time.Time) bool {
	var n int
	if scope == "subscription" {
		_ = tx.QueryRow("SELECT COUNT(*) FROM portal_subscriptions WHERE id=? AND user_id=? AND subscription_hash!='' AND revoked=0", subscription, user).Scan(&n)
	} else {
		_ = tx.QueryRow("SELECT COUNT(*) FROM portal_sessions WHERE id=? AND user_id=? AND kind='native' AND revoked=0 AND expires>? AND selected_subscription_id=?", scope, user, now.Unix(), subscription).Scan(&n)
	}
	return n == 1
}
func (s *Store) AcquireLease(node, connection, password string) (model.ConnectionLease, error) {
	var empty model.ConnectionLease
	if len(connection) < 16 || len(connection) > 128 || strings.ContainsAny(connection, "\r\n\x00") || len(password) != 43 {
		return empty, ErrUnauthorized
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var gate string
	if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_enabled'").Scan(&gate); err != nil || gate != "true" {
		return empty, ErrEntitlement
	}
	var hold string
	_ = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_recovery_hold'").Scan(&hold)
	if hold == "true" {
		return empty, ErrEntitlement
	}
	var user, scope, subscription string
	if err = tx.QueryRow("SELECT user_id,scope,subscription_id FROM portal_credentials WHERE node_id=? AND password_hash=?", node, secure.Hash(password)).Scan(&user, &scope, &subscription); err != nil {
		return empty, ErrUnauthorized
	}
	now := s.Now()
	if !activeCredentialSession(tx, user, subscription, scope, now) {
		return empty, ErrUnauthorized
	}
	e, err := userSubscription(tx, user, subscription, now)
	if err != nil {
		return empty, err
	}
	if !entitlementAllows(e, node) {
		return empty, ErrEntitlement
	}
	var usable int
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=? AND lifecycle='active' AND json_extract(config,'$.status')='ready'", node).Scan(&usable); err != nil || usable != 1 {
		return empty, ErrEntitlement
	}
	old, err := scanLease(tx.QueryRow(leaseSelect+"WHERE node_id=? AND connection_id=?", node, connection))
	if err == nil {
		if old.UserID != user || old.SessionID != scope || old.Closed || old.revoked || old.SubscriptionID != e.ID || !old.ExpiresAt.After(now) {
			return empty, ErrEntitlement
		}
		return old.ConnectionLease, nil
	}
	if err != sql.ErrNoRows {
		return empty, err
	}
	if e.Occupied >= e.MaxConnections {
		return empty, ErrSlots
	}
	rate := leaseRate(e)
	budget := leaseWindow(rate)
	if e.QuotaBytes != nil {
		available := *e.QuotaBytes - e.UsedBytes - e.ReservedBytes
		if available < budget {
			budget = available
		}
	}
	if budget < model.MinimumSubscriptionLeaseBytes {
		return empty, ErrEntitlement
	}
	l := model.ConnectionLease{ID: NewID(), UserID: user, AccountingID: "subscription_" + subscription, SubscriptionID: subscription, SessionID: scope, NodeID: node, CreatedAt: now.UTC(), ExpiresAt: leaseExpiry(now, e), BandwidthBytesPerSecond: rate, BudgetBytes: budget, Revision: e.Revision, State: "active"}
	l.LeaseID = l.ID
	if _, err = tx.Exec("INSERT INTO portal_leases(id,node_id,connection_id,user_id,session_id,subscription_id,revision,created_at,expires,bandwidth,budget) VALUES(?,?,?,?,?,?,?,?,?,?,?)", l.ID, node, connection, user, scope, subscription, e.Revision, now.Unix(), l.ExpiresAt.Unix(), 0, budget); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("UPDATE portal_usage SET reserved=reserved+? WHERE subscription_id=?", budget, subscription); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("INSERT INTO portal_rates(lease_id) VALUES(?)", l.ID); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO node_accounting_identities(node_id,accounting_id,subject_type,subject_id) VALUES(?,?,'subscription',?)", node, l.AccountingID, subscription); err != nil {
		return empty, err
	}
	if err = rebalancePortalRates(tx, subscription, e, now); err != nil {
		return empty, err
	}
	updated, err := scanLease(tx.QueryRow(leaseSelect+"WHERE id=?", l.ID))
	if err != nil {
		return empty, err
	}
	l = updated.ConnectionLease
	err = tx.Commit()
	return l, err
}
func (s *Store) RenewLease(node, id string, u model.LeaseUsage) (model.ConnectionLease, error) {
	var empty model.ConnectionLease
	if u.UploadBytes < 0 || u.DownloadBytes < 0 || u.UploadBytes > math.MaxInt64-u.DownloadBytes {
		return empty, errors.New("连接累计流量无效")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	l, err := scanLease(tx.QueryRow(leaseSelect+"WHERE id=? AND node_id=?", id, node))
	if errors.Is(err, sql.ErrNoRows) {
		return settledLeaseReceipt(tx, node, id, u)
	}
	if err != nil {
		return empty, notFound(err)
	}
	if u.UploadBytes < l.UploadBytes || u.DownloadBytes < l.DownloadBytes {
		return empty, ErrConflict
	}
	consumed := u.UploadBytes + u.DownloadBytes
	if consumed > l.BudgetBytes {
		return empty, ErrConflict
	}
	if l.Closed {
		if !u.Closed || consumed != l.ConsumedBytes {
			return empty, ErrEntitlement
		}
		return l.ConnectionLease, nil
	}
	if u.AppliedRateVersion < 0 || u.AppliedRateVersion > l.RateVersion {
		return empty, ErrConflict
	}
	if u.AppliedRateVersion == l.RateVersion {
		ceiling := l.BandwidthBytesPerSecond
		if l.RatePending {
			ceiling = 0
		} else if ceiling == 0 {
			ceiling = -1
		}
		if _, err = tx.Exec("UPDATE portal_rates SET ack=?,ceiling=? WHERE lease_id=?", u.AppliedRateVersion, ceiling, id); err != nil {
			return empty, err
		}
	}
	du, dd := u.UploadBytes-l.UploadBytes, u.DownloadBytes-l.DownloadBytes
	if _, err = tx.Exec("UPDATE portal_usage SET upload=upload+?,download=download+?,reserved=reserved-? WHERE subscription_id=?", du, dd, du+dd, l.SubscriptionID); err != nil {
		return empty, err
	}
	l.UploadBytes = u.UploadBytes
	l.DownloadBytes = u.DownloadBytes
	l.ConsumedBytes = consumed
	// Final reports can arrive after expiry; settlement never resurrects leases.
	if u.Closed {
		if _, err = tx.Exec("UPDATE portal_usage SET reserved=reserved-? WHERE subscription_id=?", l.BudgetBytes-consumed, l.SubscriptionID); err != nil {
			return empty, err
		}
		l.Closed = true
		l.State = "closed"
	} else {
		now := s.Now()
		e, eerr := userSubscription(tx, l.UserID, l.SubscriptionID, now)
		valid := eerr == nil && entitlementAllows(e, node) && !l.revoked && l.ExpiresAt.After(now) && activeCredentialSession(tx, l.UserID, l.SubscriptionID, l.SessionID, now)
		if valid {
			var rank int
			err = tx.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=? AND closed=0 AND revoked=0 AND expires>? AND (created_at<? OR (created_at=? AND rowid<=(SELECT rowid FROM portal_leases WHERE id=?)))", l.SubscriptionID, now.Unix(), l.CreatedAt.Unix(), l.CreatedAt.Unix(), l.ID).Scan(&rank)
			if err != nil {
				return empty, err
			}
			valid = rank <= e.MaxConnections
		}
		if !valid {
			if _, err = tx.Exec("UPDATE portal_leases SET upload=?,download=?,revoked=1 WHERE id=?", u.UploadBytes, u.DownloadBytes, id); err != nil {
				return empty, err
			}
			if err = tx.Commit(); err != nil {
				return empty, err
			}
			return empty, ErrEntitlement
		}
		l.Revision = e.Revision
		l.ExpiresAt = leaseExpiry(now, e)
		needed := leaseWindow(l.BandwidthBytesPerSecond) - (l.BudgetBytes - consumed)
		if needed < 0 {
			needed = 0
		}
		if e.QuotaBytes != nil {
			available := *e.QuotaBytes - e.UsedBytes - e.ReservedBytes
			if available < needed {
				needed = available
			}
			if needed < 0 {
				needed = 0
			}
		}
		if needed > 0 {
			if _, err = tx.Exec("UPDATE portal_usage SET reserved=reserved+? WHERE subscription_id=?", needed, l.SubscriptionID); err != nil {
				return empty, err
			}
			l.BudgetBytes += needed
		}
		if l.BudgetBytes <= consumed {
			if _, err = tx.Exec("UPDATE portal_leases SET upload=?,download=?,revoked=1 WHERE id=?", u.UploadBytes, u.DownloadBytes, id); err != nil {
				return empty, err
			}
			if err = tx.Commit(); err != nil {
				return empty, err
			}
			return empty, ErrEntitlement
		}
	}
	if _, err = tx.Exec("UPDATE portal_leases SET upload=?,download=?,budget=?,bandwidth=?,revision=?,expires=?,closed=? WHERE id=?", l.UploadBytes, l.DownloadBytes, l.BudgetBytes, l.BandwidthBytesPerSecond, l.Revision, l.ExpiresAt.Unix(), l.Closed, id); err != nil {
		return empty, err
	}
	e, err := userSubscription(tx, l.UserID, l.SubscriptionID, s.Now())
	if err != nil {
		return empty, err
	}
	if e != nil {
		if err = rebalancePortalRates(tx, l.SubscriptionID, e, s.Now()); err != nil {
			return empty, err
		}
	}
	updated, err := scanLease(tx.QueryRow(leaseSelect+"WHERE id=?", id))
	if err != nil {
		return empty, err
	}
	l = updated
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return l.ConnectionLease, nil
}

func settledLeaseReceipt(tx *sql.Tx, node, id string, u model.LeaseUsage) (model.ConnectionLease, error) {
	var l model.ConnectionLease
	l.ID = id
	l.LeaseID = id
	l.NodeID = node
	l.Closed = true
	l.State = "closed"
	err := tx.QueryRow("SELECT user_id,accounting_id,upload,download,budget,rate_version FROM settled_leases WHERE id=? AND node_id=?", id, node).Scan(&l.UserID, &l.AccountingID, &l.UploadBytes, &l.DownloadBytes, &l.BudgetBytes, &l.RateVersion)
	if err != nil {
		return l, notFound(err)
	}
	l.ConsumedBytes = l.UploadBytes + l.DownloadBytes
	if !u.Closed || u.UploadBytes != l.UploadBytes || u.DownloadBytes != l.DownloadBytes || u.AppliedRateVersion < 0 || u.AppliedRateVersion > l.RateVersion {
		return model.ConnectionLease{}, ErrConflict
	}
	return l, nil
}

func reconcileSubscriptionLeases(tx *sql.Tx, e *model.UserSubscription, now time.Time) error {
	rows, err := tx.Query("SELECT id,node_id FROM portal_leases WHERE subscription_id=? AND closed=0 AND revoked=0 AND expires>? ORDER BY created_at,rowid", e.ID, now.Unix())
	if err != nil {
		return err
	}
	type record struct{ id, node string }
	leases := []record{}
	for rows.Next() {
		var r record
		if err = rows.Scan(&r.id, &r.node); err != nil {
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
	retained := 0
	for _, l := range leases {
		if !entitlementAllows(e, l.node) || retained >= e.MaxConnections || e.QuotaBytes != nil && e.UsedBytes+e.ReservedBytes > *e.QuotaBytes {
			if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE id=?", l.id); err != nil {
				return err
			}
		} else {
			retained++
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
	return out, rows.Err()
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

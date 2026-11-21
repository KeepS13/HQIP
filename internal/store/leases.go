package store

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/apernet/hysteria/core/v2/billing"
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
func (s *Store) AcquireLease(node, connection, password string, offline ...bool) (model.ConnectionLease, error) {
	return s.acquireLease(node, connection, password, "", len(offline) == 1 && offline[0])
}

func (s *Store) AcquireProtocolLease(node, connection, password, protocol string, offline bool) (model.ConnectionLease, error) {
	if protocol != "hysteria2" && protocol != "vless" {
		return model.ConnectionLease{}, ErrConflict
	}
	return s.acquireLease(node, connection, password, protocol, offline)
}

func (s *Store) acquireLease(node, connection, password, protocol string, withOffline bool) (model.ConnectionLease, error) {
	if len(connection) < 16 || len(connection) > 128 || strings.ContainsAny(connection, "\r\n\x00") || len(password) != 43 {
		return model.ConnectionLease{}, ErrUnauthorized
	}
	if withOffline && !s.OfflineAdmissionEnabled(node) {
		return model.ConnectionLease{}, ErrEntitlement
	}
	return retryDatabase(func() (model.ConnectionLease, error) {
		var empty model.ConnectionLease
		transaction, err := s.leaseTransactionScope("SELECT subscription_id FROM portal_credentials WHERE node_id=? AND password_hash=?", node, secure.Hash(password))
		if err != nil {
			return empty, err
		}
		// Release the subscription queue after every rolled-back attempt, before
		// retry backoff, so another writer can make progress while this one waits.
		defer transaction.close()
		tx, err := transaction.begin()
		if err != nil {
			return empty, err
		}
		defer tx.Rollback()
		var gate string
		if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_enabled'").Scan(&gate); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return empty, err
		}
		if gate != "true" {
			return empty, ErrEntitlement
		}
		var hold string
		if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_recovery_hold'").Scan(&hold); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return empty, err
		}
		if hold == "true" {
			return empty, ErrEntitlement
		}
		var user, scope, subscription string
		if err = tx.QueryRow("SELECT user_id,scope,subscription_id FROM portal_credentials WHERE node_id=? AND password_hash=?", node, secure.Hash(password)).Scan(&user, &scope, &subscription); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return empty, ErrUnauthorized
			}
			return empty, err
		}
		now := s.Now()
		active, err := activeCredentialSession(tx, user, subscription, scope, now)
		if err != nil {
			return empty, err
		}
		if !active {
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
		if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=? AND lifecycle='active' AND json_extract(config,'$.status')='ready'", node).Scan(&usable); err != nil {
			return empty, err
		}
		if usable != 1 {
			return empty, ErrEntitlement
		}
		existing := func() (model.ConnectionLease, error) {
			old, err := scanLease(tx.QueryRow(leaseSelect+"WHERE node_id=? AND connection_id=?", node, connection))
			if err != nil {
				return empty, err
			}
			if old.UserID != user || old.SessionID != scope || old.Closed || old.revoked || old.SubscriptionID != e.ID || !old.ExpiresAt.After(now) {
				return empty, ErrEntitlement
			}
			period, err := leasePeriod(tx, old.ID)
			if err != nil {
				return empty, err
			}
			if period != e.CurrentPeriodID {
				return empty, ErrEntitlement
			}
			if old.ExpiresAt.After(e.CurrentPeriodEndsAt) {
				old.ExpiresAt = e.CurrentPeriodEndsAt
			}
			state, err := loadLeaseBilling(tx, old.ID)
			if err != nil {
				return empty, err
			}
			if state != nil {
				if state.Grant.Protocol != protocol {
					return empty, ErrConflict
				}
				old.Billing = &state.Grant
			}
			if state == nil && protocol != "" {
				return empty, ErrConflict
			}
			return old.ConnectionLease, nil
		}
		if protocol == "" {
			settings, err := readTrafficBilling(tx)
			if err != nil {
				return empty, err
			}
			if settings.Enabled {
				old, err := existing()
				if errors.Is(err, sql.ErrNoRows) {
					return empty, ErrEntitlement
				}
				return old, err
			}
		}
		rate := leaseRate(e)
		budget := leaseWindow(rate)
		if e.QuotaBytes != nil {
			available := *e.QuotaBytes - e.UsedBytes - e.ReservedBytes
			if withOffline {
				budget = available / int64(max(1, e.MaxConnections-e.Occupied))
			}
			if available < budget {
				budget = available
			}
		}
		minimum := model.MinimumSubscriptionLeaseBytes
		if protocol != "" {
			policy, err := billingPolicy(tx, protocol)
			if err != nil {
				return empty, err
			}
			minimum = billing.Ceiling(model.MinimumSubscriptionLeaseBytes * policy.Multiplier)
		}
		if e.Occupied >= e.MaxConnections || budget < minimum {
			// Replays still succeed when their existing reservation fills the
			// quota or last slot. Rejected new connections stay read-only.
			old, err := existing()
			if !errors.Is(err, sql.ErrNoRows) {
				return old, err
			}
			if e.Occupied >= e.MaxConnections {
				return empty, ErrSlots
			}
			return empty, ErrEntitlement
		}
		l := model.ConnectionLease{ID: NewID(), UserID: user, AccountingID: "subscription_" + subscription, SubscriptionID: subscription, SessionID: scope, NodeID: node, CreatedAt: now.UTC(), ExpiresAt: leaseExpiry(now, e), BandwidthBytesPerSecond: rate, BudgetBytes: budget, Revision: e.Revision, State: "active"}
		l.LeaseID = l.ID
		if withOffline {
			l.ExpiresAt = offlineExpiry(now, e)
		}
		// An absent-key SELECT before this INSERT creates a Serializable gap
		// predicate on the shared node/connection index. Let its unique constraint
		// arbitrate replays without making unrelated new connections conflict.
		inserted, err := tx.Exec("INSERT INTO portal_leases(id,node_id,connection_id,user_id,session_id,subscription_id,revision,created_at,expires,bandwidth,budget) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(node_id,connection_id) DO NOTHING", l.ID, node, connection, user, scope, subscription, e.Revision, now.Unix(), l.ExpiresAt.Unix(), 0, budget)
		if err != nil {
			return empty, err
		}
		rows, err := inserted.RowsAffected()
		if err != nil {
			return empty, err
		}
		if rows == 0 {
			return existing()
		}
		if _, err = tx.Exec("INSERT INTO subscription_lease_periods(lease_id,period_id) VALUES(?,?)", l.ID, e.CurrentPeriodID); err != nil {
			return empty, err
		}
		billingState, err := createLeaseBilling(tx, l.ID, protocol, budget)
		if err != nil {
			return empty, err
		}
		reserve := budget
		if withOffline && e.QuotaBytes == nil {
			reserve = 0
		}
		if err = updatePeriodUsage(tx, subscription, e.CurrentPeriodID, 0, 0, reserve); err != nil {
			return empty, err
		}
		if withOffline {
			if err = createOfflineGrant(tx, &l, e, secure.Hash(password), now); err != nil {
				return empty, err
			}
		}
		if billingState != nil {
			if withOffline {
				if e.QuotaBytes == nil {
					billingState.Reserved = 0
					billingState.Grant.BudgetUnits = billing.MaxRawBytes
				}
				if err = tx.QueryRow("SELECT emergency_budget FROM portal_offline_grants WHERE lease_id=?", l.ID).Scan(&billingState.Grant.EmergencyBytes); err != nil {
					return empty, err
				}
			}
			if err = saveLeaseBilling(tx, l.ID, billingState); err != nil {
				return empty, err
			}
			if _, err = tx.Exec("UPDATE portal_leases SET budget=? WHERE id=?", billing.MaxRawBytes, l.ID); err != nil {
				return empty, err
			}
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
		if billingState != nil {
			l.Billing = &billingState.Grant
		}
		err = tx.Commit()
		return l, err
	})
}
func (s *Store) RenewLease(node, id string, u model.LeaseUsage) (model.ConnectionLease, error) {
	if u.UploadBytes < 0 || u.DownloadBytes < 0 || u.UploadBytes > math.MaxInt64-u.DownloadBytes {
		return model.ConnectionLease{}, errors.New("连接累计流量无效")
	}
	return retryDatabase(func() (model.ConnectionLease, error) {
		var empty model.ConnectionLease
		scope, err := s.leaseTransactionScope("SELECT subscription_id FROM portal_leases WHERE id=? AND node_id=?", id, node)
		if err != nil {
			return empty, err
		}
		defer scope.close()
		tx, err := scope.begin()
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
		billingState, err := loadLeaseBilling(tx, id)
		if err != nil {
			return empty, err
		}
		if billingState != nil {
			l.Billing = &billingState.Grant
		}
		var offlineCount int
		if err = tx.QueryRow("SELECT COUNT(*) FROM portal_offline_grants WHERE lease_id=?", id).Scan(&offlineCount); err != nil {
			return empty, err
		}
		if offlineCount != 0 {
			return empty, ErrConflict
		}
		if u.UploadBytes < l.UploadBytes || u.DownloadBytes < l.DownloadBytes {
			return empty, ErrConflict
		}
		consumed := u.UploadBytes + u.DownloadBytes
		if consumed > l.BudgetBytes {
			return empty, ErrConflict
		}
		if l.Closed {
			if !validBillingReplay(billingState, u.Billing) {
				return empty, ErrConflict
			}
			if !u.Closed || consumed != l.ConsumedBytes {
				return empty, ErrEntitlement
			}
			return l.ConnectionLease, nil
		}
		if u.AppliedRateVersion < 0 || u.AppliedRateVersion > l.RateVersion {
			return empty, ErrConflict
		}
		// All writers for this subscription lock its quota ledger before rate
		// rows. Locking a lease's rate first can deadlock with another lease's
		// renewal while it rebalances the shared subscription rates.
		du, dd := u.UploadBytes-l.UploadBytes, u.DownloadBytes-l.DownloadBytes
		period, err := leasePeriod(tx, id)
		if err != nil {
			return empty, err
		}
		released := du + dd
		if billingState != nil {
			du, dd, released, err = settleBilling(tx, l.SubscriptionID, period, billingState, u.Billing, u.UploadBytes, u.DownloadBytes, 0)
			if err != nil {
				return empty, err
			}
		} else if len(u.Billing) > 0 {
			return empty, ErrConflict
		}
		if err = updatePeriodUsage(tx, l.SubscriptionID, period, du, dd, -released); err != nil {
			return empty, err
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
		l.UploadBytes = u.UploadBytes
		l.DownloadBytes = u.DownloadBytes
		l.ConsumedBytes = consumed
		// Final reports can arrive after expiry; settlement never resurrects leases.
		if u.Closed {
			remaining := l.BudgetBytes - consumed
			if billingState != nil {
				remaining = billingState.Reserved
				billingState.Reserved = 0
			}
			if err = updatePeriodUsage(tx, l.SubscriptionID, period, 0, 0, -remaining); err != nil {
				return empty, err
			}
			l.Closed = true
			l.State = "closed"
		} else {
			now := s.Now()
			e, eerr := userSubscription(tx, l.UserID, l.SubscriptionID, now)
			// Preserve the error that aborted the transaction. Continuing with a
			// revocation write would replace a retryable 40001/40P01 with 25P02.
			if eerr != nil && !errors.Is(eerr, ErrNotFound) {
				return empty, eerr
			}
			valid := eerr == nil && entitlementAllows(e, node) && e.CurrentPeriodID == period && !l.revoked && l.ExpiresAt.After(now)
			if valid {
				valid, err = activeCredentialSession(tx, l.UserID, l.SubscriptionID, l.SessionID, now)
				if err != nil {
					return empty, err
				}
			}
			if valid {
				var rank int
				err = tx.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=? AND closed=0 AND revoked=0 AND expires>? AND (created_at<? OR (created_at=? AND rowid<=(SELECT rowid FROM portal_leases WHERE id=?)))", l.SubscriptionID, now.Unix(), l.CreatedAt.Unix(), l.CreatedAt.Unix(), l.ID).Scan(&rank)
				if err != nil {
					return empty, err
				}
				valid = rank <= e.MaxConnections
			}
			if !valid {
				if billingState != nil {
					if err = saveLeaseBilling(tx, id, billingState); err != nil {
						return empty, err
					}
				}
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
			if billingState != nil {
				if err = issueBillingPolicy(tx, billingState); err != nil {
					return empty, err
				}
				window, err := billing.Units(leaseWindow(l.BandwidthBytesPerSecond), billingState.Grant.Multiplier)
				if err != nil {
					return empty, err
				}
				needed = billing.Ceiling(max(int64(0), window-(billingState.Grant.BudgetUnits-billingState.NormalUsed)))
			}
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
				if err = updatePeriodUsage(tx, l.SubscriptionID, period, 0, 0, needed); err != nil {
					return empty, err
				}
				if billingState != nil {
					units, err := billing.Units(needed, 100)
					if err != nil {
						return empty, err
					}
					if billingState.Grant.BudgetUnits, err = billing.Add(billingState.Grant.BudgetUnits, units); err != nil {
						return empty, err
					}
					billingState.Reserved += needed
				} else {
					l.BudgetBytes += needed
				}
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
		if billingState != nil {
			if err = saveLeaseBilling(tx, id, billingState); err != nil {
				return empty, err
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
		if billingState != nil {
			l.Billing = &billingState.Grant
		}
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return l.ConnectionLease, nil
	})
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
	state, err := loadLeaseBilling(tx, id)
	if err != nil {
		return l, err
	}
	if !validBillingReplay(state, u.Billing) {
		return l, ErrConflict
	}
	if state != nil {
		l.Billing = &state.Grant
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

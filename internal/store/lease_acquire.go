package store

import (
	"database/sql"
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/apernet/hysteria/core/v2/billing"
	"strings"
)

func (s *Store) acquireLease(node, connection, password, protocol, clientIP string, withOffline bool, quotaMode ...bool) (model.ConnectionLease, error) {
	nodeQuota := len(quotaMode) == 1 && quotaMode[0] && withOffline && protocol != ""
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
			if old.UserID != user || old.SessionID != scope || old.ClientIP != clientIP || old.Closed || old.revoked || old.SubscriptionID != e.ID || !old.ExpiresAt.After(now) {
				return empty, ErrEntitlement
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
			if nodeQuota {
				budget = available
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
		availableIP, err := onlineSlotAvailable(tx, subscription, protocol, clientIP, e.Occupied, e.MaxConnections, now)
		if err != nil {
			return empty, err
		}
		if !availableIP || budget < minimum {
			// Replays still succeed when their existing reservation fills the
			// quota or last slot. Rejected new connections stay read-only.
			old, err := existing()
			if !errors.Is(err, sql.ErrNoRows) {
				return old, err
			}
			if !availableIP {
				return empty, ErrSlots
			}
			return empty, ErrEntitlement
		}
		l := model.ConnectionLease{ClientIP: clientIP, ID: NewID(), UserID: user, AccountingID: "subscription_" + subscription, SubscriptionID: subscription, SessionID: scope, NodeID: node, CreatedAt: now.UTC(), ExpiresAt: leaseExpiry(now, e), BandwidthBytesPerSecond: rate, BudgetBytes: budget, Revision: e.Revision, State: "active"}
		l.LeaseID = l.ID
		if withOffline {
			l.ExpiresAt = offlineExpiry(now, e)
			if nodeQuota {
				l.ExpiresAt = e.ExpiresAt
			}
		}
		// An absent-key SELECT before this INSERT creates a Serializable gap
		// predicate on the shared node/connection index. Let its unique constraint
		// arbitrate replays without making unrelated new connections conflict.
		inserted, err := tx.Exec("INSERT INTO portal_leases(id,node_id,connection_id,user_id,session_id,subscription_id,revision,created_at,expires,bandwidth,budget,client_ip) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(node_id,connection_id) DO NOTHING", l.ID, node, connection, user, scope, subscription, e.Revision, now.Unix(), l.ExpiresAt.Unix(), 0, budget, clientIP)
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
		billingState, err := createLeaseBilling(tx, l.ID, protocol, budget)
		if err != nil {
			return empty, err
		}
		reserve := budget
		if withOffline && e.QuotaBytes == nil || nodeQuota {
			reserve = 0
		}
		if _, err = tx.Exec("UPDATE portal_usage SET reserved=reserved+? WHERE subscription_id=?", reserve, subscription); err != nil {
			return empty, err
		}
		if withOffline {
			if err = createOfflineGrant(tx, &l, e, secure.Hash(password), now, nodeQuota); err != nil {
				return empty, err
			}
		}
		if billingState != nil {
			if nodeQuota {
				billingState.Reserved = 0
				billingState.Grant.BudgetUnits = billing.MaxRawBytes
				billingState.NodeQuota, err = nextNodeQuotaSnapshot(tx, e, node)
				if err != nil {
					return empty, err
				}
			}
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

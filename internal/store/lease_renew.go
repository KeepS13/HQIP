package store

import (
	"database/sql"
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
	"math"
)

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
				valid, err = onlineSlotRetained(tx, l.SubscriptionID, l.ID, e.MaxConnections, now)
				if err != nil {
					return empty, err
				}
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

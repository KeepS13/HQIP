package store

import (
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func offlineExpiry(now time.Time, e *model.UserSubscription) time.Time {
	end := now.Add(model.OfflineGrace)
	if end.After(e.ExpiresAt) {
		end = e.ExpiresAt
	}
	if !e.CurrentPeriodEndsAt.IsZero() && end.After(e.CurrentPeriodEndsAt) {
		end = e.CurrentPeriodEndsAt
	}
	return end.UTC()
}

func createOfflineGrant(tx *sql.Tx, l *model.ConnectionLease, e *model.UserSubscription, hash string, now time.Time, quotaMode ...bool) error {
	if l.SessionID != "subscription" {
		var expires int64
		if err := tx.QueryRow("SELECT expires FROM portal_sessions WHERE id=?", l.SessionID).Scan(&expires); err != nil {
			return err
		}
		if l.ExpiresAt.Unix() > expires {
			l.ExpiresAt = time.Unix(expires, 0).UTC()
		}
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO portal_emergency_usage(subscription_id) VALUES(?)", e.ID); err != nil {
		return err
	}
	var used, reserved int64
	if err := tx.QueryRow("SELECT used,reserved FROM portal_emergency_usage WHERE subscription_id=?", e.ID).Scan(&used, &reserved); err != nil {
		return err
	}
	emergency := max(int64(0), model.EmergencyAllowanceBytes-used-reserved) / int64(max(1, e.MaxConnections-e.Occupied))
	normal := l.BudgetBytes
	if e.QuotaBytes == nil {
		normal, emergency = 0, 0
	}
	if len(quotaMode) == 1 && quotaMode[0] {
		normal, emergency = 0, 0
	}
	if _, err := tx.Exec("INSERT INTO portal_offline_grants(lease_id,credential_hash,authorized_at,normal_budget,normal_unlimited,emergency_budget) VALUES(?,?,?,?,?,?)", l.ID, hash, now.Unix(), normal, e.QuotaBytes == nil, emergency); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE portal_emergency_usage SET reserved=reserved+? WHERE subscription_id=?", emergency, e.ID); err != nil {
		return err
	}
	if e.QuotaBytes != nil {
		l.BudgetBytes = normal + emergency
	}
	_, err := tx.Exec("UPDATE portal_leases SET budget=?,expires=? WHERE id=?", l.BudgetBytes, l.ExpiresAt.Unix(), l.ID)
	return err
}

func loadOfflineGrant(q portalQuery, node, id string) (model.OfflineGrant, error) {
	var g model.OfflineGrant
	var authorized, until int64
	err := q.QueryRow(`SELECT l.id,l.node_id,l.user_id,l.subscription_id,l.session_id,o.credential_hash,o.authorized_at,l.expires,o.normal_budget,o.normal_unlimited,o.emergency_budget,o.emergency_used,l.upload,l.download,l.revision,r.version,l.bandwidth,r.pending,l.closed,l.revoked,o.report_version,l.client_ip FROM portal_offline_grants o JOIN portal_leases l ON l.id=o.lease_id JOIN portal_rates r ON r.lease_id=l.id WHERE l.id=? AND l.node_id=?`, id, node).Scan(&g.ID, &g.NodeID, &g.UserID, &g.SubscriptionID, &g.SessionID, &g.CredentialHash, &authorized, &until, &g.NormalBudgetBytes, &g.NormalUnlimited, &g.EmergencyBudgetBytes, &g.EmergencyUsedBytes, &g.UploadBytes, &g.DownloadBytes, &g.Revision, &g.RateVersion, &g.BandwidthBytesPerSecond, &g.RatePending, &g.Closed, &g.Revoked, &g.ReportVersion, &g.ClientIP)
	g.Version = model.OfflineAdmissionVersion
	g.AuthorizedAt, g.OfflineUntil = time.Unix(authorized, 0).UTC(), time.Unix(until, 0).UTC()
	if err == nil {
		var state *leaseBilling
		state, err = loadLeaseBilling(q, id)
		if state != nil {
			g.Billing = &state.Grant
			g.NodeQuota = state.NodeQuota
			if state.NodeQuota != nil {
				g.ConnectionSlotsVersion = model.ConnectionSlotsVersion
				g.SlotReleased = state.SlotReleased
				if !state.AuthorizationUntil.IsZero() {
					g.OfflineUntil = state.AuthorizationUntil
				}
			}
		}
	}
	return g, notFound(err)
}

func (s *Store) ReportOfflineGrant(node, id string, u model.OfflineUsage) (model.OfflineGrant, error) {
	if u.Version <= 0 || u.UploadBytes < 0 || u.DownloadBytes < 0 || u.UploadBytes > math.MaxInt64-u.DownloadBytes || u.EmergencyUsedBytes < 0 || u.EmergencyUsedBytes > u.UploadBytes+u.DownloadBytes || u.AppliedRateVersion < 0 || u.Closed && u.Active {
		return model.OfflineGrant{}, ErrConflict
	}
	extend := s.OfflineAdmissionEnabled(node)
	return retryDatabase(func() (model.OfflineGrant, error) {
		var empty model.OfflineGrant
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
		g, err := loadOfflineGrant(tx, node, id)
		if err != nil {
			return empty, err
		}
		billingState, err := loadLeaseBilling(tx, id)
		if err != nil {
			return empty, err
		}
		var previous string
		if err = tx.QueryRow("SELECT report_body FROM portal_offline_grants WHERE lease_id=?", id).Scan(&previous); err != nil {
			return empty, err
		}
		body := jsonString(u)
		if u.Version == g.ReportVersion {
			if body != previous {
				return empty, ErrConflict
			}
			return g, nil // A lost response never renews a grant twice.
		}
		if u.Version < g.ReportVersion || g.Closed || u.UploadBytes < g.UploadBytes || u.DownloadBytes < g.DownloadBytes || u.EmergencyUsedBytes < g.EmergencyUsedBytes || u.AppliedRateVersion > g.RateVersion {
			return empty, ErrConflict
		}
		total, oldTotal := u.UploadBytes+u.DownloadBytes, g.UploadBytes+g.DownloadBytes
		normal, oldNormal := total-u.EmergencyUsedBytes, oldTotal-g.EmergencyUsedBytes
		if normal < oldNormal || billingState == nil && !g.NormalUnlimited && normal > g.NormalBudgetBytes || u.EmergencyUsedBytes > g.EmergencyBudgetBytes {
			return empty, ErrConflict
		}
		normalDelta := normal - oldNormal
		period, err := leasePeriod(tx, id)
		if err != nil {
			return empty, err
		}
		uploadDelta, downloadDelta := u.UploadBytes-g.UploadBytes, u.DownloadBytes-g.DownloadBytes
		if billingState != nil {
			previousUnits := billingState.NormalUsed
			uploadDelta, downloadDelta, normalDelta, err = settleBilling(tx, g.SubscriptionID, period, billingState, u.Billing, u.UploadBytes, u.DownloadBytes, u.EmergencyUsedBytes)
			if err != nil {
				return empty, err
			}
			if billingState.NodeQuota != nil {
				if err = recordNodeQuotaUsage(tx, period, node, billingState.NormalUsed-previousUnits); err != nil {
					return empty, err
				}
			}
		} else if len(u.Billing) > 0 {
			return empty, ErrConflict
		}
		if g.NormalUnlimited {
			normalDelta = 0
			if billingState != nil {
				billingState.Reserved = 0
			}
		}
		if err = updatePeriodUsage(tx, g.SubscriptionID, period, uploadDelta, downloadDelta, -normalDelta); err != nil {
			return empty, err
		}
		if _, err = tx.Exec("UPDATE portal_emergency_usage SET used=used+?,reserved=reserved-? WHERE subscription_id=?", u.EmergencyUsedBytes-g.EmergencyUsedBytes, u.EmergencyUsedBytes-g.EmergencyUsedBytes, g.SubscriptionID); err != nil {
			return empty, err
		}
		if err = releaseUndelegatedOffline(tx, &g, billingState, u); err != nil {
			return empty, err
		}
		if u.RetainedNormalUnits != nil && g.OfflineUntil.After(s.Now()) {
			// Every child is explicitly closed or proven ended by the node.
			// Expire the connection slot while retaining unknown byte allocations.
			g.OfflineUntil = s.Now().UTC()
		}
		if u.AppliedRateVersion == g.RateVersion {
			ceiling := g.BandwidthBytesPerSecond
			if g.RatePending {
				ceiling = 0
			} else if ceiling == 0 {
				ceiling = -1
			}
			if _, err = tx.Exec("UPDATE portal_rates SET ack=?,ceiling=? WHERE lease_id=?", u.AppliedRateVersion, ceiling, id); err != nil {
				return empty, err
			}
		}
		now := s.Now()
		if err = releaseOfflineSlot(tx, &g, billingState, u, now); err != nil {
			return empty, err
		}
		e, err := userSubscription(tx, g.UserID, g.SubscriptionID, now)
		if err != nil {
			return empty, err
		}
		active, err := activeCredentialSession(tx, g.UserID, g.SubscriptionID, g.SessionID, now)
		if err != nil {
			return empty, err
		}
		var validCredential, usable int
		if err = tx.QueryRow("SELECT COUNT(*) FROM portal_credentials WHERE node_id=? AND subscription_id=? AND scope=? AND password_hash=?", node, g.SubscriptionID, g.SessionID, g.CredentialHash).Scan(&validCredential); err != nil {
			return empty, err
		}
		if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=? AND lifecycle='active' AND json_extract(config,'$.status')='ready'", node).Scan(&usable); err != nil {
			return empty, err
		}
		var gate, hold string
		if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_enabled'").Scan(&gate); err != nil {
			return empty, err
		}
		err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_recovery_hold'").Scan(&hold)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return empty, err
		}
		g.Revoked = g.Revoked || e.CurrentPeriodID != period || now.Before(g.AuthorizedAt) || !g.OfflineUntil.After(now) || !entitlementAllows(e, node) || !active || validCredential != 1 || usable != 1 || gate != "true" || hold == "true"
		// A v2-to-v3 upgrade may reveal several HY2 instances behind one IP.
		// Retain the oldest allowed slots on their next report; accounting below
		// still accepts real usage from every instance being stopped.
		if !u.Closed && !g.Revoked && !g.SlotReleased && e.Occupied > e.MaxConnections {
			retained, checkErr := onlineSlotRetained(tx, g.SubscriptionID, g.ID, e.MaxConnections, now)
			if checkErr != nil {
				return empty, checkErr
			}
			g.Revoked = !retained
		}
		if u.Closed {
			remaining := int64(0)
			if !g.NormalUnlimited {
				remaining = g.NormalBudgetBytes - normal
				if billingState != nil {
					remaining = billingState.Reserved
					billingState.Reserved = 0
				}
			}
			if err = updatePeriodUsage(tx, g.SubscriptionID, period, 0, 0, -remaining); err != nil {
				return empty, err
			}
			if _, err = tx.Exec("UPDATE portal_emergency_usage SET reserved=reserved-? WHERE subscription_id=?", g.EmergencyBudgetBytes-u.EmergencyUsedBytes, g.SubscriptionID); err != nil {
				return empty, err
			}
		} else if !g.Revoked && extend && u.Active {
			g.AuthorizedAt, g.OfflineUntil = now.UTC(), offlineExpiry(now, e)
			if g.NodeQuota != nil {
				g.OfflineUntil = e.CurrentPeriodEndsAt
			}
			if g.SessionID != "subscription" {
				var expires int64
				if err = tx.QueryRow("SELECT expires FROM portal_sessions WHERE id=?", g.SessionID).Scan(&expires); err != nil {
					return empty, err
				}
				if g.OfflineUntil.Unix() > expires {
					g.OfflineUntil = time.Unix(expires, 0).UTC()
				}
			}
			// Include this grant's unspent share before calculating a top-up.
			// Repeated heartbeats must not reserve the other connection slots'
			// shares. Billing reservations are billed bytes, not raw traffic.
			if !g.NormalUnlimited && e.QuotaBytes != nil && g.NodeQuota == nil {
				available := max(int64(0), *e.QuotaBytes-e.UsedBytes-e.ReservedBytes)
				remaining := g.NormalBudgetBytes - normal
				if billingState != nil {
					remaining = billingState.Reserved
				}
				share := (available + remaining) / int64(max(1, e.MaxConnections-e.Occupied+1))
				add := max(int64(0), share-remaining)
				if add > 0 {
					g.NormalBudgetBytes += add
					if billingState != nil {
						units, err := billing.Units(add, 100)
						if err != nil {
							return empty, err
						}
						if billingState.Grant.BudgetUnits, err = billing.Add(billingState.Grant.BudgetUnits, units); err != nil {
							return empty, err
						}
						billingState.Reserved += add
					}
					if err = updatePeriodUsage(tx, g.SubscriptionID, period, 0, 0, add); err != nil {
						return empty, err
					}
				}
			}
		}
		if err = acquireOfflineSlot(tx, &g, billingState, u, e, now); err != nil {
			return empty, err
		}
		budget := g.NormalBudgetBytes + g.EmergencyBudgetBytes
		if g.NormalUnlimited {
			budget = total
			if total <= math.MaxInt64-model.OfflineChunkBytes {
				budget += model.OfflineChunkBytes
			}
		}
		if billingState != nil {
			budget = billing.MaxRawBytes
			// A late receipt retains its original snapshot identity; replacing it
			// with the current period would relabel old delegated child budgets.
			if billingState.NodeQuota != nil && period == e.CurrentPeriodID {
				billingState.NodeQuota, err = nextNodeQuotaSnapshot(tx, e, node)
				if err != nil {
					return empty, err
				}
			}
			if !g.Revoked && !u.Closed {
				if err = issueBillingPolicy(tx, billingState); err != nil {
					return empty, err
				}
			}
			if err = saveLeaseBilling(tx, id, billingState); err != nil {
				return empty, err
			}
		}
		if _, err = tx.Exec("UPDATE portal_leases SET upload=?,download=?,budget=?,expires=?,closed=?,revoked=?,revision=? WHERE id=?", u.UploadBytes, u.DownloadBytes, budget, offlineSlotExpiry(g, now), u.Closed, g.Revoked, e.Revision, id); err != nil {
			return empty, err
		}
		lastActive := int64(0)
		if u.Active {
			lastActive = now.Unix()
		}
		if _, err = tx.Exec("UPDATE portal_offline_grants SET authorized_at=?,normal_budget=?,emergency_used=?,report_version=?,report_body=?,last_active=? WHERE lease_id=?", g.AuthorizedAt.Unix(), g.NormalBudgetBytes, u.EmergencyUsedBytes, u.Version, body, lastActive, id); err != nil {
			return empty, err
		}
		if err = rebalancePortalRates(tx, g.SubscriptionID, e, now); err != nil {
			return empty, err
		}
		result, err := loadOfflineGrant(tx, node, id)
		if err != nil {
			return empty, err
		}
		return result, tx.Commit()
	})
}

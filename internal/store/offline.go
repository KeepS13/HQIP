package store

import (
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func offlineExpiry(now time.Time, e *model.UserSubscription) time.Time {
	end := now.Add(model.OfflineGrace)
	if end.After(e.ExpiresAt) {
		end = e.ExpiresAt
	}
	return end.UTC()
}

func createOfflineGrant(tx *sql.Tx, l *model.ConnectionLease, e *model.UserSubscription, hash string, now time.Time) error {
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
	err := q.QueryRow(`SELECT l.id,l.node_id,l.user_id,l.subscription_id,l.session_id,o.credential_hash,o.authorized_at,l.expires,o.normal_budget,o.normal_unlimited,o.emergency_budget,o.emergency_used,l.upload,l.download,l.revision,r.version,l.bandwidth,r.pending,l.closed,l.revoked,o.report_version FROM portal_offline_grants o JOIN portal_leases l ON l.id=o.lease_id JOIN portal_rates r ON r.lease_id=l.id WHERE l.id=? AND l.node_id=?`, id, node).Scan(&g.ID, &g.NodeID, &g.UserID, &g.SubscriptionID, &g.SessionID, &g.CredentialHash, &authorized, &until, &g.NormalBudgetBytes, &g.NormalUnlimited, &g.EmergencyBudgetBytes, &g.EmergencyUsedBytes, &g.UploadBytes, &g.DownloadBytes, &g.Revision, &g.RateVersion, &g.BandwidthBytesPerSecond, &g.RatePending, &g.Closed, &g.Revoked, &g.ReportVersion)
	g.Version = model.OfflineAdmissionVersion
	g.AuthorizedAt, g.OfflineUntil = time.Unix(authorized, 0).UTC(), time.Unix(until, 0).UTC()
	return g, notFound(err)
}

func (s *Store) AcquireOfflineGrant(node, connection, password string) (model.OfflineGrant, error) {
	l, err := s.AcquireLease(node, connection, password, true)
	if err != nil {
		return model.OfflineGrant{}, err
	}
	return loadOfflineGrant(s.DB, node, l.ID)
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
		if normal < oldNormal || !g.NormalUnlimited && normal > g.NormalBudgetBytes || u.EmergencyUsedBytes > g.EmergencyBudgetBytes {
			return empty, ErrConflict
		}
		normalDelta := normal - oldNormal
		if g.NormalUnlimited {
			normalDelta = 0
		}
		if _, err = tx.Exec("UPDATE portal_usage SET upload=upload+?,download=download+?,reserved=reserved-? WHERE subscription_id=?", u.UploadBytes-g.UploadBytes, u.DownloadBytes-g.DownloadBytes, normalDelta, g.SubscriptionID); err != nil {
			return empty, err
		}
		if _, err = tx.Exec("UPDATE portal_emergency_usage SET used=used+?,reserved=reserved-? WHERE subscription_id=?", u.EmergencyUsedBytes-g.EmergencyUsedBytes, u.EmergencyUsedBytes-g.EmergencyUsedBytes, g.SubscriptionID); err != nil {
			return empty, err
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
		g.Revoked = g.Revoked || now.Before(g.AuthorizedAt) || !g.OfflineUntil.After(now) || !entitlementAllows(e, node) || !active || validCredential != 1 || usable != 1 || gate != "true" || hold == "true"
		if u.Closed {
			remaining := int64(0)
			if !g.NormalUnlimited {
				remaining = g.NormalBudgetBytes - normal
			}
			if _, err = tx.Exec("UPDATE portal_usage SET reserved=reserved-? WHERE subscription_id=?", remaining, g.SubscriptionID); err != nil {
				return empty, err
			}
			if _, err = tx.Exec("UPDATE portal_emergency_usage SET reserved=reserved-? WHERE subscription_id=?", g.EmergencyBudgetBytes-u.EmergencyUsedBytes, g.SubscriptionID); err != nil {
				return empty, err
			}
		} else if !g.Revoked && extend && u.Active {
			g.AuthorizedAt, g.OfflineUntil = now.UTC(), offlineExpiry(now, e)
			if g.SessionID != "subscription" {
				var expires int64
				if err = tx.QueryRow("SELECT expires FROM portal_sessions WHERE id=?", g.SessionID).Scan(&expires); err != nil {
					return empty, err
				}
				if g.OfflineUntil.Unix() > expires {
					g.OfflineUntil = time.Unix(expires, 0).UTC()
				}
			}
			// Keep each reservation's fair share topped up while online. Do not
			// take bytes already delegated to another (possibly offline) node.
			if !g.NormalUnlimited && e.QuotaBytes != nil {
				available := max(int64(0), *e.QuotaBytes-e.UsedBytes-e.ReservedBytes)
				add := available / int64(max(1, e.MaxConnections-e.Occupied+1))
				if add > 0 {
					g.NormalBudgetBytes += add
					if _, err = tx.Exec("UPDATE portal_usage SET reserved=reserved+? WHERE subscription_id=?", add, g.SubscriptionID); err != nil {
						return empty, err
					}
				}
			}
		}
		budget := g.NormalBudgetBytes + g.EmergencyBudgetBytes
		if g.NormalUnlimited {
			budget = total
			if total <= math.MaxInt64-model.OfflineChunkBytes {
				budget += model.OfflineChunkBytes
			}
		}
		if _, err = tx.Exec("UPDATE portal_leases SET upload=?,download=?,budget=?,expires=?,closed=?,revoked=?,revision=? WHERE id=?", u.UploadBytes, u.DownloadBytes, budget, g.OfflineUntil.Unix(), u.Closed, g.Revoked, e.Revision, id); err != nil {
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

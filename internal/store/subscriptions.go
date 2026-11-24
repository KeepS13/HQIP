package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func userSubscription(q portalQuery, user, id string, now time.Time) (*model.UserSubscription, error) {
	e := &model.UserSubscription{}
	var created, expires, activated int64
	var revoked, userEnabled bool
	var liveBudget bool
	var raw string
	err := q.QueryRow(`SELECT s.id,s.user_id,s.template_id,s.created_at,s.expires,s.revoked,s.revision,u.enabled,t.config,x.upload+x.download,x.reserved,COALESCE(p.validity_days,0),COALESCE(p.activated_at,0) FROM portal_subscriptions s JOIN portal_users u ON u.id=s.user_id JOIN portal_templates t ON t.id=s.template_id JOIN portal_usage x ON x.subscription_id=s.id LEFT JOIN subscription_provisioning p ON p.subscription_id=s.id WHERE s.id=? AND s.user_id=?`, id, user).Scan(&e.ID, &e.UserID, &e.TemplateID, &created, &expires, &revoked, &e.Revision, &userEnabled, &raw, &e.UsedBytes, &e.ReservedBytes, &e.ValidityDays, &activated)
	if err != nil {
		return nil, notFound(err)
	}
	var t model.SubscriptionTemplate
	if err = json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, err
	}
	e.CreatedAt = time.Unix(created, 0).UTC()
	e.ExpiresAt = time.Unix(expires, 0).UTC()
	e.ActivationPending = e.ValidityDays > 0 && activated == 0
	if e.ActivationPending {
		e.ExpiresAt = time.Time{}
	} else if activated > 0 {
		at := time.Unix(activated, 0).UTC()
		e.ActivatedAt = &at
	}
	e.TemplateName = t.Name
	e.QuotaBytes = t.QuotaBytes
	e.BandwidthMbps = t.BandwidthMbps
	e.MaxConnections = t.MaxConnections
	if err = readSubscriptionPeriods(q, e, now); err != nil {
		return nil, err
	}
	// Renewal extends this owned instance regardless of purchase/manual origin.
	// Shop visibility only controls new purchases, never an owner's renewal.
	// Exhausted traffic and occupied slots do not prevent prepaying another term;
	// neither the current ledger nor its connection limit changes on payment.
	e.Renewable = !e.ActivationPending && !revoked && userEnabled && t.Enabled && t.PriceCents != nil && expires > now.Unix()
	var allocated string
	if err = q.QueryRow("SELECT target_count,node_ids FROM subscription_allocations WHERE subscription_id=?", id).Scan(&e.NodesPerSubscription, &allocated); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(allocated), &e.NodeIDs); err != nil {
		return nil, err
	}
	if err = q.QueryRow("SELECT COALESCE(SUM(used),0),COALESCE(SUM(reserved),0) FROM portal_emergency_usage WHERE subscription_id=?", id).Scan(&e.EmergencyUsedBytes, &e.EmergencyReservedBytes); err != nil {
		return nil, err
	}
	if err = q.QueryRow("SELECT COUNT(*) FROM portal_offline_grants o JOIN portal_leases l ON l.id=o.lease_id WHERE l.subscription_id=? AND l.closed=0 AND l.expires>?", id, now.Unix()).Scan(&e.OfflineReservations); err != nil {
		return nil, err
	}
	e.Status = "active"
	if e.NodeIDs == nil {
		e.NodeIDs = []string{}
	}
	// Keep an already-authorized tail usable. Reserved bytes alone may belong
	// to a dead lease awaiting settlement and must neither count as usage nor
	// keep an unusable subscription active.
	// Match onlineSlotKey: only VLESS shares an IP slot; HY2 and unknown legacy
	// protocols use separate lease identities even when their source IP matches.
	if err = q.QueryRow("SELECT COUNT(DISTINCT CASE WHEN json_extract(json_extract(b.state,'$.grant'),'$.protocol')='vless' THEN COALESCE(NULLIF(l.client_ip,''),l.id) ELSE l.id END),COALESCE(MAX(CASE WHEN l.revoked=0 AND ((b.state IS NULL AND l.budget>l.upload+l.download) OR (b.state IS NOT NULL AND (b.remaining_units>0 OR b.remaining_emergency>0))) THEN 1 ELSE 0 END),0) FROM portal_leases l LEFT JOIN traffic_lease_billing b ON b.lease_id=l.id WHERE l.subscription_id=? AND l.closed=0 AND l.expires>?", id, now.Unix()).Scan(&e.Occupied, &liveBudget); err != nil {
		return nil, err
	}
	minimum := model.MinimumSubscriptionLeaseBytes
	settings, err := readTrafficBilling(q)
	if err != nil {
		return nil, err
	}
	if settings.Enabled {
		factor := settings.Hysteria2
		for _, node := range e.NodeIDs {
			var capability string
			if err = q.QueryRow("SELECT capabilities FROM nodes WHERE id=?", node).Scan(&capability); err != nil {
				return nil, err
			}
			if !strings.Contains(capability, billing.Capability) {
				factor = min(factor, int64(100))
			}
			var reality int
			if err = q.QueryRow("SELECT COUNT(*) FROM metadata WHERE key=?", "vless-reality:"+node).Scan(&reality); err != nil {
				return nil, err
			}
			if reality > 0 {
				factor = min(factor, settings.VLESS)
			}
		}
		minimum = billing.Ceiling(model.MinimumSubscriptionLeaseBytes * factor)
	}
	switch {
	case revoked:
		e.Status = "revoked"
	case !userEnabled || !t.Enabled:
		e.Status = "disabled"
	case e.ActivationPending:
		e.Status = "pending"
	case expires <= now.Unix():
		e.Status = "expired"
	case e.QuotaBytes != nil && (e.UsedBytes >= *e.QuotaBytes || !liveBudget && *e.QuotaBytes-e.UsedBytes < minimum):
		e.Status = "exhausted"
	}
	return e, nil
}
func (s *Store) UserSubscription(user, id string) (*model.UserSubscription, error) {
	return userSubscription(s.DB, user, id, s.Now())
}
func (s *Store) UserSubscriptions(user string) ([]model.UserSubscription, error) {
	rows, err := s.DB.Query("SELECT id FROM portal_subscriptions WHERE user_id=? ORDER BY created_at DESC,rowid DESC", user)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.UserSubscription{}
	for _, id := range ids {
		e, err := s.UserSubscription(user, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, nil
}
func validRequestKey(key string) bool {
	return len(key) >= 8 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n\x00")
}
func (s *Store) RevokeSubscription(user, id string, expected int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e, err := userSubscription(tx, user, id, s.Now())
	if err != nil {
		return err
	}
	if e.Status == "revoked" {
		return nil
	}
	if expected != e.Revision {
		return ErrConflict
	}
	if _, err = tx.Exec("UPDATE portal_subscriptions SET revoked=1,revision=revision+1,subscription_hash='',subscription_secret='' WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM portal_credentials WHERE subscription_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE subscription_id=? AND closed=0", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.revoked',?)", s.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

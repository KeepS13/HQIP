package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KeepS13/HQIP/internal/model"
)

func validSubscriptionTemplate(p model.SubscriptionTemplate) error {
	if !utf8.ValidString(p.Description) || utf8.RuneCountInString(p.Description) > 2000 {
		return errors.New("订阅介绍最多2000字")
	}
	if p.NodesPerSubscription < 1 || p.NodesPerSubscription > 1000 {
		return errors.New("每份分配节点数须为1至1000的整数")
	}
	if p.PriceCents != nil && (*p.PriceCents < 100 || *p.PriceCents > 100000000) {
		return errors.New("订阅价格须为 1 至 1000000 元，最多两位小数")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 100 || p.ValidityDays < 1 || p.ValidityDays > 36500 || p.MaxConnections < 1 || p.MaxConnections > 1000 || len(p.NodeIDs) > 1000 {
		return errors.New("订阅名称、有效天数、节点或连接数无效")
	}
	if p.BandwidthMbps != nil && (*p.BandwidthMbps <= 0 || *p.BandwidthMbps > 1e6 || math.IsNaN(*p.BandwidthMbps) || math.IsInf(*p.BandwidthMbps, 0)) {
		return errors.New("带宽必须为正数或无限")
	}
	if p.QuotaBytes != nil && (*p.QuotaBytes <= 0 || *p.QuotaBytes > 1<<60) {
		return errors.New("额度必须为正数或无限")
	}
	seen := map[string]bool{}
	for _, id := range p.NodeIDs {
		if id == "" || seen[id] {
			return errors.New("订阅节点无效")
		}
		seen[id] = true
	}
	return nil
}

func subscriptionTemplate(q portalQuery, id string) (model.SubscriptionTemplate, error) {
	var p model.SubscriptionTemplate
	var raw string
	err := q.QueryRow("SELECT config FROM portal_templates WHERE id=?", id).Scan(&raw)
	if err != nil {
		return p, notFound(err)
	}
	err = json.Unmarshal([]byte(raw), &p)
	normalizeSubscriptionTemplate(&p)
	return p, err
}
func (s *Store) SubscriptionTemplate(id string) (model.SubscriptionTemplate, error) {
	return subscriptionTemplate(s.DB, id)
}
func (s *Store) SubscriptionTemplates(listedOnly bool) ([]model.SubscriptionTemplate, error) {
	rows, err := s.DB.Query("SELECT config FROM portal_templates ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SubscriptionTemplate{}
	for rows.Next() {
		var raw string
		var p model.SubscriptionTemplate
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		normalizeSubscriptionTemplate(&p)
		if !listedOnly || p.Listed && p.Enabled {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}
func (s *Store) SaveSubscriptionTemplate(id string, p model.SubscriptionTemplate, expected int64) (model.SubscriptionTemplate, error) {
	normalizeSubscriptionTemplate(&p)
	p.Description = strings.TrimSpace(strings.ReplaceAll(p.Description, "\r\n", "\n"))
	if err := validSubscriptionTemplate(p); err != nil {
		return p, err
	}
	return retryDatabase(func() (model.SubscriptionTemplate, error) {
		// A retried transaction must not persist the previous attempt's response.
		p.AllocationShortfalls = nil
		tx, err := s.DB.Begin()
		if err != nil {
			return p, err
		}
		defer tx.Rollback()
		for _, node := range p.NodeIDs {
			var caps string
			if err = tx.QueryRow("SELECT capabilities FROM nodes WHERE id=? AND lifecycle='active'", node).Scan(&caps); err != nil {
				return p, notFound(err)
			}
			if !containsCapability(caps, "admission-v1") {
				return p, errors.New("节点须支持连接准入与订阅限制")
			}
		}
		var previous model.SubscriptionTemplate
		if id != "" {
			previous, err = subscriptionTemplate(tx, id)
			if err != nil {
				return p, err
			}
		}
		p.ID = id
		p.Revision = 1
		if id == "" {
			p.ID = NewID()
		} else {
			var revision int64
			if err = tx.QueryRow("SELECT revision FROM portal_templates WHERE id=?", id).Scan(&revision); err != nil {
				return p, notFound(err)
			}
			if revision != expected {
				return p, ErrConflict
			}
			p.Revision = revision + 1
		}
		if p.NodeIDs == nil {
			p.NodeIDs = []string{}
		}
		if _, err = tx.Exec("INSERT INTO portal_templates(id,config,revision) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config,revision=excluded.revision", p.ID, jsonString(p), p.Revision); err != nil {
			return p, err
		}
		nodesChanged := !sameNodeSet(previous.NodeIDs, p.NodeIDs)
		entitlementsChanged := nodesChanged || previous.Enabled != p.Enabled || previous.MaxConnections != p.MaxConnections || !reflect.DeepEqual(previous.QuotaBytes, p.QuotaBytes) || !reflect.DeepEqual(previous.BandwidthMbps, p.BandwidthMbps)
		if !entitlementsChanged {
			if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.template_saved',?)", s.Now().Unix(), p.ID); err != nil {
				return p, err
			}
			return p, tx.Commit()
		}
		if nodesChanged && id != "" {
			holders, shortfalls, e := reconcileNodeAllocations(tx, p, s.Now())
			if e != nil {
				return p, e
			}
			if e = persistNodeAllocations(tx, holders); e != nil {
				return p, e
			}
			p.AllocationShortfalls = shortfalls
		}
		if _, err = tx.Exec("UPDATE portal_subscriptions SET revision=revision+1 WHERE template_id=?", p.ID); err != nil {
			return p, err
		}
		if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id IN (SELECT user_id FROM portal_subscriptions WHERE template_id=?)", p.ID); err != nil {
			return p, err
		}
		rows, err := tx.Query("SELECT user_id,id FROM portal_subscriptions WHERE template_id=?", p.ID)
		if err != nil {
			return p, err
		}
		type member struct{ user, id string }
		members := []member{}
		for rows.Next() {
			var m member
			if err = rows.Scan(&m.user, &m.id); err != nil {
				rows.Close()
				return p, err
			}
			members = append(members, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return p, err
		}
		for _, m := range members {
			e, eerr := userSubscription(tx, m.user, m.id, s.Now())
			if eerr != nil {
				return p, eerr
			}
			if err = reconcileSubscriptionLeases(tx, e, s.Now()); err != nil {
				return p, err
			}
		}
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.template_saved',?)", s.Now().Unix(), p.ID); err != nil {
			return p, err
		}
		return p, tx.Commit()
	})
}

func userSubscription(q portalQuery, user, id string, now time.Time) (*model.UserSubscription, error) {
	e := &model.UserSubscription{}
	var created, expires int64
	var revoked, userEnabled bool
	var liveBudget bool
	var raw string
	err := q.QueryRow(`SELECT s.id,s.user_id,s.template_id,s.created_at,s.expires,s.revoked,s.revision,u.enabled,t.config,x.upload+x.download,x.reserved FROM portal_subscriptions s JOIN portal_users u ON u.id=s.user_id JOIN portal_templates t ON t.id=s.template_id JOIN portal_usage x ON x.subscription_id=s.id WHERE s.id=? AND s.user_id=?`, id, user).Scan(&e.ID, &e.UserID, &e.TemplateID, &created, &expires, &revoked, &e.Revision, &userEnabled, &raw, &e.UsedBytes, &e.ReservedBytes)
	if err != nil {
		return nil, notFound(err)
	}
	var t model.SubscriptionTemplate
	if err = json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, err
	}
	e.CreatedAt = time.Unix(created, 0).UTC()
	e.ExpiresAt = time.Unix(expires, 0).UTC()
	e.TemplateName = t.Name
	e.QuotaBytes = t.QuotaBytes
	e.BandwidthMbps = t.BandwidthMbps
	e.MaxConnections = t.MaxConnections
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
	if err = q.QueryRow("SELECT COUNT(*),COALESCE(MAX(CASE WHEN revoked=0 AND budget>upload+download THEN 1 ELSE 0 END),0) FROM portal_leases WHERE subscription_id=? AND closed=0 AND expires>?", id, now.Unix()).Scan(&e.Occupied, &liveBudget); err != nil {
		return nil, err
	}
	switch {
	case revoked:
		e.Status = "revoked"
	case !userEnabled || !t.Enabled:
		e.Status = "disabled"
	case expires <= now.Unix():
		e.Status = "expired"
	case e.QuotaBytes != nil && (e.UsedBytes >= *e.QuotaBytes || !liveBudget && *e.QuotaBytes-e.UsedBytes < model.MinimumSubscriptionLeaseBytes):
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
func grantSubscription(tx *sql.Tx, user, template, key string, expectedUser, expectedTemplate int64, now time.Time) (model.UserSubscription, error) {
	return grantSubscriptionWithValidity(tx, user, template, key, expectedUser, expectedTemplate, now, 0)
}

// Paid orders retain purchased duration and node count; live service limits follow the template.
func grantSubscriptionWithValidity(tx *sql.Tx, user, template, key string, expectedUser, expectedTemplate int64, now time.Time, validityDays int) (model.UserSubscription, error) {
	var empty model.UserSubscription
	if err := lockAllocationUser(tx, user); err != nil {
		return empty, err
	}
	var existing, oldTemplate string
	err := tx.QueryRow("SELECT id,template_id FROM portal_subscriptions WHERE user_id=? AND grant_key=?", user, key).Scan(&existing, &oldTemplate)
	if err == nil {
		if oldTemplate != template {
			return empty, ErrConflict
		}
		e, err := userSubscription(tx, user, existing, now)
		if err != nil {
			return empty, err
		}
		return *e, nil
	}
	if err != sql.ErrNoRows {
		return empty, err
	}
	var enabled bool
	var revision int64
	if err = tx.QueryRow("SELECT enabled,revision FROM portal_users WHERE id=?", user).Scan(&enabled, &revision); err != nil {
		return empty, notFound(err)
	}
	if !enabled {
		return empty, ErrEntitlement
	}
	if expectedUser >= 0 && revision != expectedUser {
		return empty, ErrConflict
	}
	p, err := subscriptionTemplate(tx, template)
	if err != nil {
		return empty, err
	}
	if !p.Enabled {
		return empty, ErrEntitlement
	}
	if p.Revision != expectedTemplate {
		return empty, ErrConflict
	}
	var allocated []string
	target := p.NodesPerSubscription
	if strings.HasPrefix(key, "payment:") {
		orderID := strings.TrimPrefix(key, "payment:")
		var raw string
		if err = tx.QueryRow("SELECT template_snapshot FROM payment_orders WHERE id=?", orderID).Scan(&raw); err != nil {
			return empty, err
		}
		var snapshot model.SubscriptionTemplate
		if err = json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return empty, err
		}
		normalizeSubscriptionTemplate(&snapshot)
		target = snapshot.NodesPerSubscription
		var stored int
		if e := tx.QueryRow("SELECT target_count FROM payment_node_reservations WHERE order_id=?", orderID).Scan(&stored); e == nil {
			target = stored
		} else if !errors.Is(e, sql.ErrNoRows) {
			return empty, e
		}
		allocated, err = reserveOrderNodes(tx, orderID, user, p, target, now)
	} else {
		allocated, err = allocateSubscriptionNodes(tx, user, p, now)
	}
	if err != nil {
		return empty, err
	}
	id := NewID()
	if validityDays == 0 {
		validityDays = p.ValidityDays
	}
	if validityDays < 1 || validityDays > 36500 {
		return empty, errors.New("订阅有效期无效")
	}
	expires := now.Add(time.Duration(validityDays) * 24 * time.Hour)
	if _, err = tx.Exec("INSERT INTO portal_subscriptions(id,user_id,template_id,created_at,expires,grant_key) VALUES(?,?,?,?,?,?)", id, user, template, now.Unix(), expires.Unix(), key); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("INSERT INTO subscription_allocations(subscription_id,target_count,node_ids) VALUES(?,?,?)", id, target, jsonString(allocated)); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("INSERT INTO portal_usage(subscription_id) VALUES(?)", id); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user); err != nil {
		return empty, err
	}
	e, err := userSubscription(tx, user, id, now)
	if err != nil {
		return empty, err
	}
	return *e, nil
}
func (s *Store) AssignSubscription(user, template, key string, expectedUser, expectedTemplate int64) (model.UserSubscription, error) {
	return retryDatabase(func() (model.UserSubscription, error) {
		var empty model.UserSubscription
		if !validRequestKey(key) {
			return empty, errors.New("请求标识无效")
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return empty, err
		}
		defer tx.Rollback()
		e, err := grantSubscription(tx, user, template, "admin:"+key, expectedUser, expectedTemplate, s.Now())
		if err != nil {
			return empty, err
		}
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.assigned',?)", s.Now().Unix(), e.ID); err != nil {
			return empty, err
		}
		return e, tx.Commit()
	})
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

const subscriptionRequestSelect = `SELECT r.id,r.user_id,u.email,r.template_id,t.config,r.status,r.requested_at,r.decided_at,r.note,r.subscription_id,r.revision FROM portal_requests r JOIN portal_users u ON u.id=r.user_id JOIN portal_templates t ON t.id=r.template_id `

func scanSubscriptionRequest(row rowScanner) (model.SubscriptionRequest, error) {
	var r model.SubscriptionRequest
	var raw string
	var requested, decided int64
	err := row.Scan(&r.ID, &r.UserID, &r.Email, &r.TemplateID, &raw, &r.Status, &requested, &decided, &r.Note, &r.SubscriptionID, &r.Revision)
	if err != nil {
		return r, notFound(err)
	}
	var p model.SubscriptionTemplate
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return r, err
	}
	r.TemplateName = p.Name
	r.RequestedAt = time.Unix(requested, 0).UTC()
	if decided > 0 {
		at := time.Unix(decided, 0).UTC()
		r.DecidedAt = &at
	}
	return r, nil
}
func (s *Store) SubscriptionRequests(user string) ([]model.SubscriptionRequest, error) {
	query := subscriptionRequestSelect
	args := []any{}
	if user != "" {
		query += "WHERE r.user_id=? "
		args = append(args, user)
	}
	query += "ORDER BY r.requested_at DESC,r.rowid DESC LIMIT 1000"
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SubscriptionRequest{}
	for rows.Next() {
		r, err := scanSubscriptionRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) CreateSubscriptionRequest(user, template, key string, expectedTemplate int64) (model.SubscriptionRequest, error) {
	return retryDatabase(func() (model.SubscriptionRequest, error) {
		var empty model.SubscriptionRequest
		if !validRequestKey(key) {
			return empty, errors.New("请求标识无效")
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return empty, err
		}
		defer tx.Rollback()
		r, err := scanSubscriptionRequest(tx.QueryRow(subscriptionRequestSelect+"WHERE r.user_id=? AND r.request_key=?", user, key))
		if err == nil {
			if r.TemplateID != template {
				return empty, ErrConflict
			}
			return r, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return empty, err
		}
		var enabled bool
		if err = tx.QueryRow("SELECT enabled FROM portal_users WHERE id=?", user).Scan(&enabled); err != nil {
			return empty, notFound(err)
		}
		if !enabled {
			return empty, ErrUnauthorized
		}
		p, err := subscriptionTemplate(tx, template)
		if err != nil {
			return empty, err
		}
		if !p.Enabled || !p.Listed {
			return empty, ErrEntitlement
		}
		if p.Revision != expectedTemplate {
			return empty, ErrConflict
		}
		id := NewID()
		if _, err = tx.Exec("INSERT INTO portal_requests(id,user_id,template_id,status,requested_at,request_key) VALUES(?,?,?,'pending',?,?)", id, user, template, s.Now().Unix(), key); err != nil {
			return empty, err
		}
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.requested',?)", s.Now().Unix(), id); err != nil {
			return empty, err
		}
		r, err = scanSubscriptionRequest(tx.QueryRow(subscriptionRequestSelect+"WHERE r.id=?", id))
		if err != nil {
			return empty, err
		}
		return r, tx.Commit()
	})
}
func (s *Store) DecideSubscriptionRequest(id, action, note string, expectedRequest, expectedTemplate int64) (model.SubscriptionRequest, error) {
	return retryDatabase(func() (model.SubscriptionRequest, error) {
		var empty model.SubscriptionRequest
		if action != "approve" && action != "reject" {
			return empty, errors.New("审批操作无效")
		}
		if !utf8.ValidString(note) || utf8.RuneCountInString(note) > 2000 {
			return empty, errors.New("备注最多2000个字符")
		}
		target := "approved"
		if action == "reject" {
			target = "rejected"
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return empty, err
		}
		defer tx.Rollback()
		r, err := scanSubscriptionRequest(tx.QueryRow(subscriptionRequestSelect+"WHERE r.id=?", id))
		if err != nil {
			return empty, err
		}
		if r.Status == target {
			return r, nil
		}
		if r.Status != "pending" || r.Revision != expectedRequest {
			return empty, ErrConflict
		}
		p, err := subscriptionTemplate(tx, r.TemplateID)
		if err != nil {
			return empty, err
		}
		if p.Revision != expectedTemplate {
			return empty, ErrConflict
		}
		sub := ""
		if action == "approve" {
			e, eerr := grantSubscription(tx, r.UserID, r.TemplateID, "request:"+id, -1, expectedTemplate, s.Now())
			if eerr != nil {
				return empty, eerr
			}
			sub = e.ID
		}
		if _, err = tx.Exec("UPDATE portal_requests SET status=?,decided_at=?,note=?,subscription_id=?,revision=revision+1 WHERE id=?", target, s.Now().Unix(), strings.TrimSpace(note), sub, id); err != nil {
			return empty, err
		}
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,?,?)", s.Now().Unix(), "subscription.request_"+target, id); err != nil {
			return empty, err
		}
		r, err = scanSubscriptionRequest(tx.QueryRow(subscriptionRequestSelect+"WHERE r.id=?", id))
		if err != nil {
			return empty, err
		}
		return r, tx.Commit()
	})
}
func (s *Store) CancelSubscriptionRequest(user, id string, expected int64) error {
	return retryDatabaseError(func() error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		r, err := scanSubscriptionRequest(tx.QueryRow(subscriptionRequestSelect+"WHERE r.id=? AND r.user_id=?", id, user))
		if err != nil {
			return err
		}
		if r.Status == "cancelled" {
			return nil
		}
		if r.Status != "pending" || r.Revision != expected {
			return ErrConflict
		}
		if _, err = tx.Exec("UPDATE portal_requests SET status='cancelled',decided_at=?,revision=revision+1 WHERE id=?", s.Now().Unix(), id); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.request_cancelled',?)", s.Now().Unix(), id); err != nil {
			return err
		}
		return tx.Commit()
	})
}

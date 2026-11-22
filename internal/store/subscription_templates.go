package store

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
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
		return errors.New("订阅名称、有效天数、节点或同时在线上限无效")
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

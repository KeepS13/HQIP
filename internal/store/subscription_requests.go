package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KeepS13/HQIP/internal/model"
)

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

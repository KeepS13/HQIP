package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// ErrSubscriptionAssignmentUnconfirmed means a grant committed but its response
// could not be read. Callers must recover with the original request key, even
// if the underlying read error resembles a business rejection.
var ErrSubscriptionAssignmentUnconfirmed = errors.New("subscription assignment result unconfirmed")

func grantSubscription(tx *sql.Tx, user, template, key string, expectedUser, expectedTemplate int64, now time.Time) (model.UserSubscription, error) {
	return grantSubscriptionWithValidity(tx, user, template, key, expectedUser, expectedTemplate, now, 0)
}

// Paid orders retain purchased duration and node count; live service limits follow the template.
func grantSubscriptionWithValidity(tx *sql.Tx, user, template, key string, expectedUser, expectedTemplate int64, now time.Time, validityDays int) (model.UserSubscription, error) {
	id, _, err := createSubscriptionGrant(tx, user, template, key, expectedUser, expectedTemplate, now, validityDays)
	if err != nil {
		return model.UserSubscription{}, err
	}
	value, err := userSubscription(tx, user, id, now)
	if err != nil {
		return model.UserSubscription{}, err
	}
	return *value, nil
}

// createSubscriptionGrant owns the grant, node allocation and initial period in
// the caller's transaction. Lock the user before checking request identity and
// inventory. The returned created flag is false on replay, so audit is emitted
// once. It never reads live lease/usage totals merely to construct a response.
func createSubscriptionGrant(tx *sql.Tx, user, template, key string, expectedUser, expectedTemplate int64, now time.Time, validityDays int) (string, bool, error) {
	if err := lockAllocationUser(tx, user); err != nil {
		return "", false, err
	}
	var existing, oldTemplate string
	err := tx.QueryRow("SELECT id,template_id FROM portal_subscriptions WHERE user_id=? AND grant_key=?", user, key).Scan(&existing, &oldTemplate)
	if err == nil {
		if oldTemplate != template {
			return "", false, ErrConflict
		}
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}
	var enabled bool
	var revision int64
	if err = tx.QueryRow("SELECT enabled,revision FROM portal_users WHERE id=?", user).Scan(&enabled, &revision); err != nil {
		return "", false, notFound(err)
	}
	if !enabled {
		return "", false, ErrEntitlement
	}
	if expectedUser >= 0 && revision != expectedUser {
		return "", false, ErrConflict
	}
	p, err := subscriptionTemplate(tx, template)
	if err != nil {
		return "", false, err
	}
	if !p.Enabled {
		return "", false, ErrEntitlement
	}
	if p.Revision != expectedTemplate {
		return "", false, ErrConflict
	}
	var allocated []string
	target := p.NodesPerSubscription
	if strings.HasPrefix(key, "payment:") {
		orderID := strings.TrimPrefix(key, "payment:")
		var raw string
		if err = tx.QueryRow("SELECT template_snapshot FROM payment_orders WHERE id=?", orderID).Scan(&raw); err != nil {
			return "", false, err
		}
		var snapshot model.SubscriptionTemplate
		if err = json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return "", false, err
		}
		normalizeSubscriptionTemplate(&snapshot)
		target = snapshot.NodesPerSubscription
		var stored int
		if e := tx.QueryRow("SELECT target_count FROM payment_node_reservations WHERE order_id=?", orderID).Scan(&stored); e == nil {
			target = stored
		} else if !errors.Is(e, sql.ErrNoRows) {
			return "", false, e
		}
		allocated, err = reserveOrderNodes(tx, orderID, user, p, target, now)
	} else {
		allocated, err = allocateSubscriptionNodes(tx, user, p, now)
	}
	if err != nil {
		return "", false, err
	}
	id := NewID()
	if validityDays == 0 {
		validityDays = p.ValidityDays
	}
	if validityDays < 1 || validityDays > 36500 {
		return "", false, errors.New("订阅有效期无效")
	}
	expires := now.Add(time.Duration(validityDays) * 24 * time.Hour)
	if _, err = tx.Exec("INSERT INTO portal_subscriptions(id,user_id,template_id,created_at,expires,grant_key) VALUES(?,?,?,?,?,?)", id, user, template, now.Unix(), expires.Unix(), key); err != nil {
		return "", false, err
	}
	if _, err = tx.Exec("INSERT INTO subscription_allocations(subscription_id,target_count,node_ids) VALUES(?,?,?)", id, target, jsonString(allocated)); err != nil {
		return "", false, err
	}
	if _, err = tx.Exec("INSERT INTO portal_usage(subscription_id) VALUES(?)", id); err != nil {
		return "", false, err
	}
	if _, err = tx.Exec("INSERT INTO subscription_periods(id,subscription_id,starts_at,ends_at) VALUES(?,?,?,?)", id, id, now.Unix(), expires.Unix()); err != nil {
		return "", false, err
	}
	if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// AssignSubscription atomically grants once per user/request key. Serializable
// retries repeat only rolled-back database work. Read the presentation after
// commit: concurrent metering must not abort an otherwise complete new grant.
// If that read or the commit response fails, retrying the same key recovers the
// original subscription, even when the caller still has the old revisions.
func (s *Store) AssignSubscription(user, template, key string, expectedUser, expectedTemplate int64) (model.UserSubscription, error) {
	if !validRequestKey(key) {
		return model.UserSubscription{}, errors.New("请求标识无效")
	}
	id, err := retryDatabase(func() (string, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return "", err
		}
		defer tx.Rollback()
		id, created, err := createSubscriptionGrant(tx, user, template, "admin:"+key, expectedUser, expectedTemplate, s.Now(), 0)
		if err != nil {
			return "", err
		}
		if created {
			if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.assigned',?)", s.Now().Unix(), id); err != nil {
				return "", err
			}
		}
		return id, tx.Commit()
	})
	if err != nil {
		return model.UserSubscription{}, err
	}
	value, err := s.UserSubscription(user, id)
	if err != nil {
		return model.UserSubscription{}, fmt.Errorf("%w: %w", ErrSubscriptionAssignmentUnconfirmed, err)
	}
	return *value, nil
}

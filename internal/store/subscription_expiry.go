package store

import (
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrSubscriptionExpired = errors.New("订阅已到期，不能调整到期时间")
var ErrSubscriptionRemoved = errors.New("订阅已移除，不能调整到期时间")
var ErrSubscriptionExpiry = errors.New("新的到期时间必须晚于服务器当前时间")

// UpdateSubscriptionExpiry changes only the lifetime of one existing grant.
// Usage, allocations, credentials, orders and sessions retain their identities.
func (s *Store) UpdateSubscriptionExpiry(user, id string, expires time.Time, expected int64) (model.UserSubscription, error) {
	expires = expires.UTC().Truncate(time.Second)
	return retryDatabase(func() (model.UserSubscription, error) {
		var empty model.UserSubscription
		tx, err := s.DB.Begin()
		if err != nil {
			return empty, err
		}
		defer tx.Rollback()
		// Serialize with grants and allocation changes for this user. Capture
		// the clock after waiting, so an expired grant cannot be resurrected.
		if err = lockAllocationUser(tx, user); err != nil {
			return empty, err
		}
		current, err := userSubscription(tx, user, id, s.Now())
		if err != nil {
			return empty, err
		}
		if current.Status == "revoked" {
			return empty, ErrSubscriptionRemoved
		}
		validate := func() error {
			now := s.Now()
			// Status can be disabled or exhausted; expiry is independent.
			if !current.ExpiresAt.After(now) {
				return ErrSubscriptionExpired
			}
			if !expires.After(now) {
				return ErrSubscriptionExpiry
			}
			return nil
		}
		if err = validate(); err != nil {
			return empty, err
		}
		if current.Revision != expected {
			return empty, ErrConflict
		}
		if current.ExpiresAt.Equal(expires) {
			return *current, nil
		}
		result, err := tx.Exec(`UPDATE portal_subscriptions SET expires=?,revision=revision+1
WHERE id=? AND user_id=? AND revision=? AND revoked=0 AND expires>?`, expires.Unix(), id, user, expected, s.Now().Unix())
		if err != nil {
			return empty, err
		}
		if count, err := result.RowsAffected(); err != nil {
			return empty, err
		} else if count != 1 {
			return empty, ErrConflict
		}
		if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user); err != nil {
			return empty, err
		}
		audit := jsonString(map[string]any{"userId": user, "subscriptionId": id, "previousExpiresAt": current.ExpiresAt, "expiresAt": expires, "revision": expected + 1})
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.expiry_updated',?)", s.Now().Unix(), audit); err != nil {
			return empty, err
		}
		updated, err := userSubscription(tx, user, id, s.Now())
		if err != nil {
			return empty, err
		}
		// Recheck after all database work, including retries and lock waits.
		if err = validate(); err != nil {
			return empty, err
		}
		return *updated, tx.Commit()
	})
}

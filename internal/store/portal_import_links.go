package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/KeepS13/HQIP/internal/secure"
)

// A combined URL carries existing per-subscription capabilities, never a new
// account-wide credential. Bound its request-line size and export work. Tokens
// contain no dots, so the envelope is unambiguous and needs no database schema.
const maxImportSubscriptions = 100

// PortalSubscription returns or changes one owner's import capability. Create
// is idempotent; rotation/revocation invalidate both URL and proxy credentials
// atomically. Native sessions and unsettled usage reservations are preserved.
func (s *Store) PortalSubscription(user, subscription, action string) (string, error) {
	// Only the explicit all-subscriptions API may broaden the requested scope.
	if subscription == "" {
		return "", ErrNotFound
	}
	return s.portalImportLink(user, subscription, action)
}

// PortalAllSubscriptions snapshots all currently active subscriptions in one
// link. Rotate/revoke also invalidate older, now-inactive third-party links.
func (s *Store) PortalAllSubscriptions(user, action string) (string, error) {
	return s.portalImportLink(user, "", action)
}

func (s *Store) portalImportLink(user, subscription, action string) (string, error) {
	if action != "get" && action != "create" && action != "rotate" && action != "revoke" {
		return "", errors.New("导入链接操作无效")
	}
	return retryDatabase(func() (string, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return "", err
		}
		defer tx.Rollback()
		// Serialize with purchases and other import writes at the user row before
		// reading subscriptions; never take a user lock after updating a child.
		if action != "get" {
			if err = lockAllocationUser(tx, user); err != nil {
				return "", err
			}
		}
		ids, err := s.importSubscriptionIDs(tx, user, subscription, action)
		if err != nil {
			return "", err
		}
		if subscription == "" && (action == "rotate" || action == "revoke") {
			if err = revokeImportCredentials(tx, user, ""); err != nil {
				return "", err
			}
		}
		tokens := []string{}
		missing := false
		for _, id := range ids {
			token, e := s.importSubscriptionToken(tx, user, id, action)
			if e != nil {
				return "", e
			}
			missing = missing || token == ""
			tokens = append(tokens, token)
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		if len(tokens) == 0 || missing || action == "revoke" {
			return "", nil
		}
		return s.BaseURL + "/usub/" + strings.Join(tokens, "."), nil
	})
}

func (s *Store) importSubscriptionIDs(tx *sql.Tx, user, subscription, action string) ([]string, error) {
	if subscription != "" {
		e, err := userSubscription(tx, user, subscription, s.Now())
		if err != nil {
			return nil, err
		}
		if e.Status != "active" && e.Status != "pending" && action != "revoke" {
			if action == "get" {
				return nil, nil
			}
			return nil, ErrEntitlement
		}
		return []string{subscription}, nil
	}
	if action == "revoke" {
		return nil, nil
	}
	rows, err := tx.Query("SELECT s.id FROM portal_subscriptions s LEFT JOIN subscription_provisioning p ON p.subscription_id=s.id WHERE s.user_id=? AND s.revoked=0 AND (s.expires>? OR p.activated_at=0) ORDER BY s.id", user, s.Now().Unix())
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
	active := []string{}
	for _, id := range ids {
		e, err := userSubscription(tx, user, id, s.Now())
		if err != nil {
			return nil, err
		}
		if e.Status == "active" || e.Status == "pending" {
			active = append(active, id)
		}
	}
	if len(active) > maxImportSubscriptions {
		return nil, errors.New("有效订阅超过合并上限，请选择单份订阅")
	}
	if len(active) == 0 && action != "get" {
		return nil, ErrEntitlement
	}
	return active, nil
}

func (s *Store) importSubscriptionToken(tx *sql.Tx, user, subscription, action string) (string, error) {
	var enc string
	if err := tx.QueryRow("SELECT subscription_secret FROM portal_subscriptions WHERE id=? AND user_id=?", subscription, user).Scan(&enc); err != nil {
		return "", notFound(err)
	}
	if action == "get" || action == "create" && enc != "" {
		if enc == "" {
			return "", nil
		}
		return s.Vault.Decrypt(enc)
	}
	if err := revokeImportCredentials(tx, user, subscription); err != nil {
		return "", err
	}
	if action == "revoke" {
		return "", nil
	}
	token := secure.Token()
	enc, err := s.Vault.Encrypt(token)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec("UPDATE portal_subscriptions SET subscription_hash=?,subscription_secret=? WHERE id=? AND user_id=?", secure.Hash(token), enc, subscription, user)
	return token, err
}

// This transaction marks live leases revoked without releasing reserved bytes:
// Agent renewal/reporting must stop forwarding and settle final usage first.
func revokeImportCredentials(tx *sql.Tx, user, subscription string) error {
	filter, args := "user_id=?", []any{user}
	if subscription != "" {
		filter += " AND subscription_id=?"
		args = append(args, subscription)
	}
	if _, err := tx.Exec("DELETE FROM portal_credentials WHERE "+filter+" AND scope='subscription'", args...); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE portal_leases SET revoked=1 WHERE "+filter+" AND session_id='subscription' AND closed=0", args...); err != nil {
		return err
	}
	subFilter := strings.ReplaceAll(filter, "subscription_id", "id")
	if _, err := tx.Exec("UPDATE portal_subscriptions SET subscription_hash='',subscription_secret='',revision=revision+1 WHERE "+subFilter, args...); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user)
	return err
}

// ResolvePortalImport validates the complete envelope. A single revoked member
// invalidates an old combined link; it must never silently acquire replacement
// credentials. Cross-account and duplicate capabilities are rejected.
func (s *Store) ResolvePortalImport(token string) (string, []string, error) {
	if len(token) > maxImportSubscriptions*44-1 {
		return "", nil, ErrUnauthorized
	}
	tokens := strings.Split(token, ".")
	user, ids, seen := "", []string{}, map[string]bool{}
	for _, item := range tokens {
		owner, id, err := s.ResolvePortalSubscription(item)
		if err != nil || seen[id] || user != "" && user != owner {
			return "", nil, ErrUnauthorized
		}
		user, seen[id] = owner, true
		ids = append(ids, id)
	}
	return user, ids, nil
}

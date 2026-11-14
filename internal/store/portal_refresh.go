package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

var refreshRequestRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{32,128}$`)

// A durable request ID permits retrying one rotation after a lost response.
// Different IDs still trigger reuse detection, and revoked sessions never replay.
func (s *Store) PortalRefreshRequest(token, requestID string) (model.PortalTokens, error) {
	var t model.PortalTokens
	if len(token) != 43 || (requestID != "" && !refreshRequestRE.MatchString(requestID)) {
		return t, ErrUnauthorized
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	now := s.Now()
	hash := secure.Hash(token)
	if requestID != "" {
		var encrypted string
		err = tx.QueryRow(`SELECT r.response FROM portal_refresh_receipts r JOIN portal_sessions x ON x.id=r.session_id JOIN portal_users u ON u.id=x.user_id
WHERE r.hash=? AND r.request_hash=? AND r.expires>? AND x.refresh_hash=r.current_hash AND x.revoked=0 AND x.expires>? AND u.enabled=1`, hash, secure.Hash(requestID), now.Unix(), now.Unix()).Scan(&encrypted)
		if err == nil {
			plain, e := s.Vault.Decrypt(encrypted)
			if e != nil {
				return t, secretError(e)
			}
			if e = json.Unmarshal([]byte(plain), &t); e != nil {
				return t, e
			}
			if e = tx.Commit(); e != nil {
				return model.PortalTokens{}, e
			}
			return t, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return t, err
		}
	}
	var user, id string
	var expires int64
	err = tx.QueryRow("SELECT x.user_id,x.id,x.expires FROM portal_sessions x JOIN portal_users u ON u.id=x.user_id WHERE x.refresh_hash=? AND x.kind='native' AND x.revoked=0 AND x.expires>? AND u.enabled=1", hash, now.Unix()).Scan(&user, &id, &expires)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return t, err
		}
		var spent string
		err = tx.QueryRow("SELECT session_id FROM portal_spent_refresh WHERE hash=?", hash).Scan(&spent)
		if err == nil {
			if _, err = tx.Exec("UPDATE portal_sessions SET revoked=1 WHERE id=?", spent); err != nil {
				return t, err
			}
			if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE session_id=?", spent); err != nil {
				return t, err
			}
			if err = tx.Commit(); err != nil {
				return t, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return t, err
		}
		return t, ErrUnauthorized
	}
	t = model.PortalTokens{AccessToken: secure.Token(), RefreshToken: secure.Token(), SessionID: id, AccessExpiresAt: now.Add(15 * time.Minute), RefreshExpiresAt: time.Unix(expires, 0).UTC()}
	if _, err = tx.Exec("INSERT INTO portal_spent_refresh(hash,session_id,expires) VALUES(?,?,?)", hash, id, expires); err != nil {
		return t, err
	}
	result, err := tx.Exec("UPDATE portal_sessions SET access_hash=?,refresh_hash=?,access_expires=?,last_seen=? WHERE id=? AND refresh_hash=? AND revoked=0", secure.Hash(t.AccessToken), secure.Hash(t.RefreshToken), t.AccessExpiresAt.Unix(), now.Unix(), id, hash)
	if err != nil {
		return t, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return t, ErrConflict
	}
	// Read the user before commit: a failed post-commit read must not lose rotation.
	if err = tx.QueryRow("SELECT id,email FROM portal_users WHERE id=? AND enabled=1", user).Scan(&t.User.ID, &t.User.Email); err != nil {
		return t, err
	}
	if requestID != "" {
		encoded, e := s.Vault.Encrypt(jsonString(t))
		if e != nil {
			return t, e
		}
		if _, err = tx.Exec("INSERT INTO portal_refresh_receipts(hash,request_hash,session_id,response,current_hash,expires) VALUES(?,?,?,?,?,?)", hash, secure.Hash(requestID), id, encoded, secure.Hash(t.RefreshToken), now.Add(10*time.Minute).Unix()); err != nil {
			return t, err
		}
	}
	if err = tx.Commit(); err != nil {
		return model.PortalTokens{}, err
	}
	return t, nil
}

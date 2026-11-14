package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func newPortalSession(tx *sql.Tx, user, kind, name, installation string, now time.Time) (model.PortalTokens, string, error) {
	t := model.PortalTokens{AccessToken: secure.Token(), RefreshToken: secure.Token(), SessionID: NewID(), AccessExpiresAt: now.Add(15 * time.Minute), RefreshExpiresAt: now.Add(30 * 24 * time.Hour)}
	csrf := secure.Token()
	if kind == "browser" {
		t.AccessExpiresAt = now.Add(24 * time.Hour)
		t.RefreshExpiresAt = t.AccessExpiresAt
	}
	// Bounded active session count; login cannot create unbounded rows silently.
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM portal_sessions WHERE user_id=? AND revoked=0 AND expires>?", user, now.Unix()).Scan(&count); err != nil {
		return t, "", err
	}
	if count >= 64 {
		return t, "", errors.New("登录会话已达上限，请先撤销旧会话")
	}
	if kind == "native" {
		if _, err := tx.Exec("UPDATE portal_sessions SET revoked=1 WHERE user_id=? AND kind='native' AND installation_id=?", user, installation); err != nil {
			return t, "", err
		}
	}
	_, err := tx.Exec("INSERT INTO portal_sessions(id,user_id,kind,client_name,installation_id,access_hash,refresh_hash,csrf,access_expires,expires,created_at,last_seen) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", t.SessionID, user, kind, name, installation, secure.Hash(t.AccessToken), secure.Hash(t.RefreshToken), csrf, t.AccessExpiresAt.Unix(), t.RefreshExpiresAt.Unix(), now.Unix(), now.Unix())
	return t, csrf, err
}
func (s *Store) PortalLogin(email, password string) (model.PortalTokens, string, error) {
	var t model.PortalTokens
	email, err := normalizeEmail(email)
	if err != nil {
		return t, "", ErrUnauthorized
	}
	var id, hash string
	var enabled bool
	if err = s.DB.QueryRow("SELECT id,password,enabled FROM portal_users WHERE email=?", email).Scan(&id, &hash, &enabled); err != nil || !enabled || !secure.CheckPassword(hash, password) {
		return t, "", ErrUnauthorized
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return t, "", err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM portal_users WHERE id=? AND enabled=1 AND password=?", id, hash).Scan(&count); err != nil || count != 1 {
		return t, "", ErrUnauthorized
	}
	t, csrf, err := newPortalSession(tx, id, "browser", "网页会话", "", s.Now())
	if err != nil {
		return t, "", err
	}
	if err = tx.Commit(); err != nil {
		return t, "", err
	}
	t.User, err = s.PortalUser(id)
	return t, csrf, err
}
func (s *Store) PortalIdentity(token, kind string) (user, session, csrf string, err error) {
	if len(token) != 43 {
		return "", "", "", ErrUnauthorized
	}
	err = s.DB.QueryRow("SELECT x.user_id,x.id,x.csrf FROM portal_sessions x JOIN portal_users u ON u.id=x.user_id WHERE x.access_hash=? AND x.kind=? AND x.revoked=0 AND x.access_expires>? AND x.expires>? AND u.enabled=1", secure.Hash(token), kind, s.Now().Unix(), s.Now().Unix()).Scan(&user, &session, &csrf)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", ErrUnauthorized
		}
		return "", "", "", err
	}
	_, err = s.DB.Exec("UPDATE portal_sessions SET last_seen=? WHERE id=?", s.Now().Unix(), session)
	return
}
func (s *Store) RevokePortalSession(user, id string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.Exec("UPDATE portal_sessions SET revoked=1 WHERE id=? AND user_id=?", id, user)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE user_id=? AND session_id=?", user, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) PortalSessions(user, current string) ([]model.PortalSession, error) {
	now := s.Now().Unix()
	// Rank before the history limit so old, still-valid devices remain visible.
	rows, err := s.DB.Query(`SELECT id,kind,client_name,created_at,last_seen,expires,revoked
		FROM portal_sessions WHERE user_id=?
		ORDER BY CASE WHEN revoked=0 AND expires>? THEN CASE WHEN id=? THEN 0 ELSE 1 END ELSE 2 END,
		last_seen DESC,created_at DESC,id ASC LIMIT 100`, user, now, current)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PortalSession{}
	for rows.Next() {
		var v model.PortalSession
		var created, last, expires int64
		if err = rows.Scan(&v.ID, &v.Kind, &v.ClientName, &created, &last, &expires, &v.Revoked); err != nil {
			return nil, err
		}
		v.CreatedAt = time.Unix(created, 0).UTC()
		v.LastSeen = time.Unix(last, 0).UTC()
		v.ExpiresAt = time.Unix(expires, 0).UTC()
		v.Current = v.ID == current && !v.Revoked && expires > now
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) PortalRefresh(token string) (model.PortalTokens, error) {
	return s.PortalRefreshRequest(token, "")
}

var pkceRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var verifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/callback" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return true
}
func (s *Store) BeginPortalAuthorization(challenge, redirect, state, name, installation string) (string, time.Time, error) {
	if !pkceRE.MatchString(challenge) || !validRedirect(redirect) || len(state) < 16 || len(state) > 256 || len(name) < 1 || len(name) > 100 || len(installation) < 8 || len(installation) > 128 || strings.ContainsAny(name, "\r\n\x00") {
		return "", time.Time{}, errors.New("桌面授权参数无效")
	}
	id := secure.Token()
	expires := s.Now().Add(5 * time.Minute)
	tx, err := s.DB.Begin()
	if err != nil {
		return "", expires, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM portal_authorizations WHERE expires<?", s.Now().Unix()); err != nil {
		return "", expires, err
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM portal_authorizations").Scan(&count); err != nil {
		return "", expires, err
	}
	if count >= 10000 {
		return "", expires, errors.New("授权请求繁忙")
	}
	_, err = tx.Exec("INSERT INTO portal_authorizations(id,challenge,redirect_uri,state,installation_id,client_name,expires) VALUES(?,?,?,?,?,?,?)", id, challenge, redirect, state, installation, name, expires.Unix())
	if err != nil {
		return "", expires, err
	}
	err = tx.Commit()
	return id, expires, err
}
func (s *Store) PortalAuthorization(id string) (string, time.Time, error) {
	var name string
	var expires int64
	err := s.DB.QueryRow("SELECT client_name,expires FROM portal_authorizations WHERE id=? AND expires>? AND consumed=0 AND code_hash=''", id, s.Now().Unix()).Scan(&name, &expires)
	return name, time.Unix(expires, 0).UTC(), notFound(err)
}
func (s *Store) ApprovePortalAuthorization(id, user, session string) (string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// The HTTP middleware's identity may predate logout, password reset or
	// account suspension. Recheck it in the same transaction as the approval.
	now := s.Now().Unix()
	var active int
	if err = tx.QueryRow("SELECT COUNT(*) FROM portal_sessions x JOIN portal_users u ON u.id=x.user_id WHERE x.id=? AND x.user_id=? AND x.kind='browser' AND x.revoked=0 AND x.access_expires>? AND x.expires>? AND u.enabled=1", session, user, now, now).Scan(&active); err != nil {
		return "", err
	}
	if active != 1 {
		return "", ErrUnauthorized
	}
	var redirect, state string
	if err = tx.QueryRow("SELECT redirect_uri,state FROM portal_authorizations WHERE id=? AND expires>? AND consumed=0 AND code_hash=''", id, now).Scan(&redirect, &state); err != nil {
		return "", ErrNotFound
	}
	code := secure.Token()
	if _, err = tx.Exec("UPDATE portal_authorizations SET user_id=?,code_hash=? WHERE id=?", user, secure.Hash(code), id); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	u, _ := url.Parse(redirect)
	q := u.Query()
	q.Set("code", code)
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (s *Store) ExchangePortalCode(code, verifier, redirect, installation string) (model.PortalTokens, error) {
	var t model.PortalTokens
	if !verifierRE.MatchString(verifier) || len(code) != 43 || !validRedirect(redirect) {
		return t, ErrUnauthorized
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	var id, user, challenge, name string
	err = tx.QueryRow("SELECT a.id,a.user_id,a.challenge,a.client_name FROM portal_authorizations a JOIN portal_users u ON u.id=a.user_id WHERE a.code_hash=? AND a.redirect_uri=? AND a.installation_id=? AND a.expires>? AND a.consumed=0 AND u.enabled=1", secure.Hash(code), redirect, installation, s.Now().Unix()).Scan(&id, &user, &challenge, &name)
	if err != nil {
		return t, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(verifier))
	if !secure.Equal(challenge, base64.RawURLEncoding.EncodeToString(sum[:])) {
		return t, ErrUnauthorized
	}
	if _, err = tx.Exec("UPDATE portal_authorizations SET consumed=1 WHERE id=?", id); err != nil {
		return t, err
	}
	t, _, err = newPortalSession(tx, user, "native", name, installation, s.Now())
	if err != nil {
		return t, err
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	t.User, err = s.PortalUser(user)
	return t, err
}

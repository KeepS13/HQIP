package store

import (
	"database/sql"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

var ErrEntitlement = errors.New("订阅不可用、已到期、额度不足或连接已撤销")
var ErrSlots = errors.New("并发连接数已满，请先断开其他连接")

type portalQuery interface{ QueryRow(string, ...any) *sql.Row }

func (s *Store) PortalEnabled() bool {
	var v string
	_ = s.DB.QueryRow("SELECT value FROM metadata WHERE key='portal_enabled'").Scan(&v)
	return v == "true"
}
func (s *Store) PortalSetting(key string) string {
	var v string
	_ = s.DB.QueryRow("SELECT value FROM metadata WHERE key=?", key).Scan(&v)
	return v
}
func (s *Store) SetPortalSetting(key, value string) error {
	_, err := s.DB.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func (s *Store) ActivatePortal() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		"UPDATE metadata SET value='true' WHERE key='portal_enabled'",
		"UPDATE devices SET enabled=0",
		"UPDATE client_sessions SET revoked=1",
		"UPDATE removals SET immediate=1 WHERE status NOT IN ('revoked','cancelled')",
		"UPDATE nodes SET desired_revision=desired_revision+1",
	} {
		if _, err = tx.Exec(query); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		s.Audit("portal.activate", "legacy-device-auth-disabled")
	}
	return err
}
func normalizeEmail(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || len(v) > 254 || !strings.Contains(v, "@") {
		return "", errors.New("邮箱格式无效")
	}
	return v, nil
}
func portalPassword(v string) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) >= 6 && len(v) <= 1024
}
func (s *Store) RegisterPortal(email, password string) (model.PortalUser, error) {
	var u model.PortalUser
	email, err := normalizeEmail(email)
	if err != nil {
		return u, err
	}
	if !portalPassword(password) {
		return u, errors.New("密码至少需要 6 个字符，且不能超过 1024 字节")
	}
	u = model.PortalUser{ID: NewID(), Email: email, Enabled: true, CreatedAt: s.Now().UTC(), Revision: 1, Subscriptions: []model.UserSubscription{}}
	hash := secure.HashPassword(password)
	tx, err := s.DB.Begin()
	if err != nil {
		return u, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO portal_users(id,email,password,created_at) VALUES(?,?,?,?)", u.ID, email, hash, u.CreatedAt.Unix()); err != nil {
		return u, errors.New("无法创建账号，请检查邮箱是否已使用")
	}
	err = tx.Commit()
	return u, err
}
func (s *Store) PortalUser(id string) (model.PortalUser, error) {
	var u model.PortalUser
	var created int64
	err := s.DB.QueryRow("SELECT id,email,enabled,created_at,revision FROM portal_users WHERE id=?", id).Scan(&u.ID, &u.Email, &u.Enabled, &created, &u.Revision)
	u.CreatedAt = time.Unix(created, 0).UTC()
	if err != nil {
		return u, notFound(err)
	}
	u.Subscriptions, err = s.UserSubscriptions(id)
	return u, err
}
func (s *Store) PortalUsers() ([]model.PortalUser, error) {
	rows, err := s.DB.Query("SELECT id FROM portal_users ORDER BY created_at DESC")
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
	out := []model.PortalUser{}
	for _, id := range ids {
		u, e := s.PortalUser(id)
		if e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, nil
}
func (s *Store) SetPortalEnabled(id string, enabled bool, expected ...int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRow("SELECT revision FROM portal_users WHERE id=?", id).Scan(&revision); err != nil {
		return notFound(err)
	}
	if len(expected) > 0 && expected[0] != revision {
		return ErrConflict
	}
	res, err := tx.Exec("UPDATE portal_users SET enabled=?,revision=revision+1 WHERE id=?", enabled, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if !enabled {
		if _, err = tx.Exec("UPDATE portal_authorizations SET consumed=1 WHERE user_id=?", id); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE portal_sessions SET revoked=1 WHERE user_id=?", id); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE user_id=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) PortalPassword(id, current, next string, admin bool) error {
	if !portalPassword(next) {
		return errors.New("密码至少需要 6 个字符，且不能超过 1024 字节")
	}
	var old string
	if err := s.DB.QueryRow("SELECT password FROM portal_users WHERE id=?", id).Scan(&old); err != nil {
		return err
	}
	if !admin {
		if valid, _ := checkPortalPassword(old, current); !valid {
			return ErrUnauthorized
		}
	}
	hash := secure.HashPassword(next)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE portal_users SET password=?,revision=revision+1 WHERE id=? AND password=?", hash, id, old)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec("UPDATE portal_authorizations SET consumed=1 WHERE user_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE portal_sessions SET revoked=1 WHERE user_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE user_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM portal_credentials WHERE user_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

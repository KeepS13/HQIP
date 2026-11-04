package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

var ErrEntitlement = errors.New("套餐不可用、已到期、额度不足或连接已撤销")
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
	if _, err = tx.Exec(`UPDATE metadata SET value='true' WHERE key='portal_enabled'; UPDATE devices SET enabled=0; UPDATE client_sessions SET revoked=1; UPDATE removals SET immediate=1 WHERE status NOT IN ('revoked','cancelled'); UPDATE nodes SET desired_revision=desired_revision+1;`); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.Audit("portal.activate", "legacy-device-auth-disabled")
	}
	return err
}
func validPlan(p model.Plan) error {
	if len(strings.TrimSpace(p.Name)) < 1 || len(p.Name) > 100 || p.MaxConnections < 1 || p.MaxConnections > 1000 || len(p.NodeIDs) > 1000 {
		return errors.New("套餐名称、节点或连接数无效")
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
			return errors.New("套餐节点无效")
		}
		seen[id] = true
	}
	if len(seen) > 0 && !seen[p.DefaultNodeID] || len(seen) == 0 && p.DefaultNodeID != "" {
		return errors.New("默认节点必须属于套餐")
	}
	return nil
}
func (s *Store) SavePlan(id string, p model.Plan, expected ...int64) (model.Plan, error) {
	if err := validPlan(p); err != nil {
		return p, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	for _, n := range p.NodeIDs {
		var capabilities string
		if err = tx.QueryRow("SELECT capabilities FROM nodes WHERE id=? AND lifecycle='active'", n).Scan(&capabilities); err == sql.ErrNoRows {
			return p, errors.New("套餐包含不存在或已退役节点")
		} else if err != nil {
			return p, err
		}
		if !containsCapability(capabilities, "admission-v1") {
			return p, errors.New("套餐节点须先升级支持连接准入与套餐限制")
		}
	}
	p.ID = id
	p.Revision = 1
	if id == "" {
		p.ID = NewID()
	} else {
		if err = tx.QueryRow("SELECT revision+1 FROM portal_plans WHERE id=?", id).Scan(&p.Revision); err != nil {
			return p, notFound(err)
		}
		if len(expected) > 0 && expected[0] != p.Revision-1 {
			return p, ErrConflict
		}
	}
	raw, _ := json.Marshal(p)
	_, err = tx.Exec("INSERT INTO portal_plans(id,config,revision) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config,revision=excluded.revision", p.ID, string(raw), p.Revision)
	if err != nil {
		return p, err
	}
	_, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE plan_id=?", p.ID)
	if err != nil {
		return p, err
	}
	if err = tx.Commit(); err == nil {
		s.Audit("plan.save", p.ID)
	}
	return p, err
}
func (s *Store) Plans() ([]model.Plan, error) {
	rows, err := s.DB.Query("SELECT config FROM portal_plans ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Plan{}
	for rows.Next() {
		var raw string
		var p model.Plan
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) PlanPreview(id string) (int, int, error) {
	var users, leases int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM portal_users WHERE plan_id=?", id).Scan(&users)
	if err == nil {
		err = s.DB.QueryRow("SELECT COUNT(*) FROM portal_leases l JOIN portal_users u ON u.id=l.user_id WHERE u.plan_id=? AND l.closed=0 AND l.expires>?", id, s.Now().Unix()).Scan(&leases)
	}
	return users, leases, err
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
	u = model.PortalUser{ID: NewID(), Email: email, Enabled: true, CreatedAt: s.Now().UTC()}
	hash := secure.HashPassword(password)
	tx, err := s.DB.Begin()
	if err != nil {
		return u, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO portal_users(id,email,password,created_at) VALUES(?,?,?,?)", u.ID, email, hash, u.CreatedAt.Unix()); err != nil {
		return u, errors.New("无法创建账号，请检查邮箱是否已使用")
	}
	if _, err = tx.Exec("INSERT INTO portal_usage(user_id,epoch) VALUES(?,1)", u.ID); err != nil {
		return u, err
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
	u.Entitlement, err = s.Entitlement(id)
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
func (s *Store) Entitlement(id string) (*model.Entitlement, error) {
	return entitlement(s.DB, id, s.Now())
}
func entitlement(q portalQuery, id string, now time.Time) (*model.Entitlement, error) {
	var e model.Entitlement
	var expires int64
	var enabled bool
	var raw string
	err := q.QueryRow("SELECT u.plan_id,u.expires,u.epoch,u.revision,u.enabled,COALESCE(p.config,''),x.upload+x.download,x.reserved FROM portal_users u JOIN portal_usage x ON x.user_id=u.id AND x.epoch=u.epoch LEFT JOIN portal_plans p ON p.id=u.plan_id WHERE u.id=?", id).Scan(&e.PlanID, &expires, &e.Epoch, &e.Revision, &enabled, &raw, &e.UsedBytes, &e.ReservedBytes)
	if err != nil {
		return nil, notFound(err)
	}
	if e.PlanID == "" {
		return nil, nil
	}
	var p model.Plan
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	e.ID = e.PlanID
	e.PlanName = p.Name
	e.QuotaBytes = p.QuotaBytes
	e.BandwidthMbps = p.BandwidthMbps
	e.MaxConnections = p.MaxConnections
	e.NodeIDs = p.NodeIDs
	e.DefaultNodeID = p.DefaultNodeID
	e.Status = "active"
	t := time.Unix(expires, 0).UTC()
	e.ExpiresAt = &t
	if err = q.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE user_id=? AND closed=0 AND expires>?", id, now.Unix()).Scan(&e.Occupied); err != nil {
		return nil, err
	}
	if !enabled || !p.Enabled {
		e.Status = "disabled"
	} else if expires <= now.Unix() {
		e.Status = "expired"
	} else if e.QuotaBytes != nil && e.UsedBytes >= *e.QuotaBytes {
		e.Status = "exhausted"
	}
	return &e, nil
}
func (s *Store) AssignPortal(id, plan string, expires time.Time, reset bool, expected ...int64) error {
	if !expires.After(s.Now()) || expires.After(s.Now().AddDate(100, 0, 0)) {
		return errors.New("到期时间须为未来时间")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var planRaw string
	if err = tx.QueryRow("SELECT config FROM portal_plans WHERE id=?", plan).Scan(&planRaw); err != nil {
		return notFound(err)
	}
	var target model.Plan
	if err = json.Unmarshal([]byte(planRaw), &target); err != nil {
		return err
	}
	var epoch, revision int64
	var oldPlan string
	if err = tx.QueryRow("SELECT epoch,revision,plan_id FROM portal_users WHERE id=?", id).Scan(&epoch, &revision, &oldPlan); err != nil {
		return notFound(err)
	}
	if len(expected) > 0 && expected[0] != revision {
		return ErrConflict
	}
	if !target.Enabled {
		return errors.New("停用套餐不能开通或续期")
	}
	if !target.SaleEnabled && (oldPlan != plan || reset) {
		return errors.New("停售套餐不能新开通、重开或换入")
	}
	if reset {
		epoch++
		if _, err = tx.Exec("INSERT INTO portal_usage(user_id,epoch) VALUES(?,?)", id, epoch); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE user_id=?", id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE portal_users SET plan_id=?,expires=?,epoch=?,revision=revision+1 WHERE id=?", plan, expires.Unix(), epoch, id); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.Audit("user.assignment", id)
	}
	return err
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
	if !admin && !secure.CheckPassword(old, current) {
		return ErrUnauthorized
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

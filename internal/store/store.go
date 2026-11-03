package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	_ "modernc.org/sqlite"
)

var Shanghai, _ = time.LoadLocation("Asia/Shanghai")
var ErrNotFound = errors.New("记录不存在")

type Store struct {
	DB      *sql.DB
	Vault   *secure.Vault
	BaseURL string
	DataDir string
	Now     func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS admin(id INTEGER PRIMARY KEY CHECK(id=1),username TEXT NOT NULL,password TEXT NOT NULL,must_change INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY,csrf TEXT NOT NULL,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY,config TEXT NOT NULL,password TEXT NOT NULL DEFAULT '',password_expires INTEGER NOT NULL DEFAULT 0,agent_hash TEXT NOT NULL UNIQUE,agent_secret TEXT NOT NULL,desired_revision INTEGER NOT NULL DEFAULT 1,applied_revision INTEGER NOT NULL DEFAULT 0,last_seen INTEGER NOT NULL DEFAULT 0,metrics TEXT NOT NULL DEFAULT '{}',online TEXT NOT NULL DEFAULT '{}',core_healthy INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS devices(id TEXT PRIMARY KEY,name TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,created_at INTEGER NOT NULL,token_hash TEXT NOT NULL UNIQUE,token_secret TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS credentials(node_id TEXT NOT NULL REFERENCES nodes(id),device_id TEXT NOT NULL,password TEXT NOT NULL,PRIMARY KEY(node_id,device_id));
CREATE TABLE IF NOT EXISTS jobs(id INTEGER PRIMARY KEY AUTOINCREMENT,node_id TEXT NOT NULL REFERENCES nodes(id),kind TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',created_at INTEGER NOT NULL,started_at INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_job ON jobs(node_id) WHERE state IN ('pending','running');
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,node_id TEXT NOT NULL REFERENCES nodes(id),stage TEXT NOT NULL,message TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS events_node ON events(node_id,id);
CREATE TABLE IF NOT EXISTS daily(node_id TEXT NOT NULL,device_id TEXT NOT NULL,start INTEGER NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,PRIMARY KEY(node_id,device_id,start));
CREATE TABLE IF NOT EXISTS minute(node_id TEXT NOT NULL,device_id TEXT NOT NULL,start INTEGER NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,PRIMARY KEY(node_id,device_id,start));
CREATE TABLE IF NOT EXISTS hourly(node_id TEXT NOT NULL,device_id TEXT NOT NULL,start INTEGER NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,PRIMARY KEY(node_id,device_id,start));
CREATE TABLE IF NOT EXISTS lifetime(node_id TEXT NOT NULL,device_id TEXT NOT NULL,upload INTEGER NOT NULL DEFAULT 0,download INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(node_id,device_id));
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT,at INTEGER NOT NULL,action TEXT NOT NULL,subject TEXT NOT NULL);
INSERT OR IGNORE INTO metadata(key,value) VALUES('schema_version','1');
`

func Open(dataDir, baseURL string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	if _, dbErr := os.Stat(filepath.Join(dataDir, "hqip.db")); dbErr == nil {
		if _, keyErr := os.Stat(filepath.Join(dataDir, "master.key")); os.IsNotExist(keyErr) {
			return nil, errors.New("数据库已存在但主密钥缺失，请从同一份备份恢复 master.key")
		}
	}
	vault, err := secure.Open(filepath.Join(dataDir, "master.key"))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "hqip.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=10000", "PRAGMA synchronous=FULL", schema} {
		if _, err = db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = EnsureCloudflareSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(filepath.Join(dataDir, "hqip.db"), 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, Vault: vault, BaseURL: strings.TrimRight(baseURL, "/"), DataDir: dataDir, Now: time.Now}, nil
}

func (s *Store) Close() error      { return s.DB.Close() }
func jsonString(v any) string      { b, _ := json.Marshal(v); return string(b) }
func NewID() string                { return secure.Token()[:18] }
func ProbeID(nodeID string) string { return "__probe_" + nodeID }
func DayStart(t time.Time) int64 {
	t = t.In(Shanghai)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Shanghai).Unix()
}
func MonthStart(t time.Time) int64 {
	t = t.In(Shanghai)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, Shanghai).Unix()
}
func (s *Store) Audit(action, subject string) {
	_, _ = s.DB.Exec("INSERT INTO audit(at,action,subject) VALUES(?,?,?)", s.Now().Unix(), action, subject)
}

func (s *Store) InitAdmin(username, password string) error {
	if username == "" || len(username) > 80 || len(password) < 12 {
		return errors.New("管理员名称不能为空，密码至少 12 个字符")
	}
	_, err := s.DB.Exec("INSERT INTO admin(id,username,password,must_change) VALUES(1,?,?,1)", username, secure.HashPassword(password))
	if err != nil {
		return errors.New("管理员已初始化，不能覆盖现有账号")
	}
	return nil
}
func (s *Store) Admin() (username, hash string, must bool, err error) {
	err = s.DB.QueryRow("SELECT username,password,must_change FROM admin WHERE id=1").Scan(&username, &hash, &must)
	return
}
func (s *Store) ChangePassword(password string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE admin SET password=?,must_change=0 WHERE id=1", secure.HashPassword(password)); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CreateSession() (token, csrf string, err error) {
	token = secure.Token()
	csrf = secure.Token()
	_, err = s.DB.Exec("INSERT INTO sessions(hash,csrf,expires) VALUES(?,?,?)", secure.Hash(token), csrf, s.Now().Add(12*time.Hour).Unix())
	return
}
func (s *Store) Session(token string) (csrf string, err error) {
	err = s.DB.QueryRow("SELECT csrf FROM sessions WHERE hash=? AND expires>?", secure.Hash(token), s.Now().Unix()).Scan(&csrf)
	return
}
func (s *Store) DeleteSession(token string) {
	_, _ = s.DB.Exec("DELETE FROM sessions WHERE hash=?", secure.Hash(token))
}

func (s *Store) Maintenance() error {
	now := s.Now()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []struct {
		query string
		arg   any
	}{
		{"UPDATE nodes SET password='',password_expires=0 WHERE password_expires>0 AND password_expires<?", now.Unix()},
		{"DELETE FROM sessions WHERE expires<?", now.Unix()},
		{"DELETE FROM spent_refresh WHERE expires<?", now.Unix()},
		{"DELETE FROM minute WHERE start<?", now.AddDate(0, 0, -7).Unix()},
		{"DELETE FROM hourly WHERE start<?", now.AddDate(0, 0, -90).Unix()},
		{"DELETE FROM daily WHERE start<?", DayStart(now.AddDate(0, -13, 0))},
		{"DELETE FROM events WHERE created_at<?", now.AddDate(0, 0, -30).Unix()},
		{"DELETE FROM audit WHERE at<?", now.AddDate(0, -13, 0).Unix()},
		{"DELETE FROM jobs WHERE state IN ('done','error') AND created_at<?", now.AddDate(0, 0, -30).Unix()},
	} {
		if _, err = tx.Exec(q.query, q.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Event(nodeID, stage, message string) error {
	if len(message) > 2000 {
		message = message[:2000]
	}
	_, err := s.DB.Exec("INSERT INTO events(node_id,stage,message,created_at) VALUES(?,?,?,?)", nodeID, stage, message, s.Now().Unix())
	return err
}
func (s *Store) Events(nodeID string, after int64) ([]model.TaskEvent, error) {
	rows, err := s.DB.Query("SELECT id,stage,message,created_at FROM events WHERE node_id=? AND id>? ORDER BY id LIMIT 500", nodeID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.TaskEvent{}
	for rows.Next() {
		var e model.TaskEvent
		var at int64
		e.NodeID = nodeID
		if err = rows.Scan(&e.ID, &e.Stage, &e.Message, &at); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(at, 0).UTC()
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) Snapshot(ctx context.Context, destination string) error {
	// VACUUM INTO produces a consistent SQLite snapshot including WAL transactions.
	_, err := s.DB.ExecContext(ctx, "VACUUM INTO ?", destination)
	return err
}

// A restored controller becomes authoritative using a fresh revision epoch.
// Milliseconds remain exactly representable by JavaScript's JSON number type.
// Existing agent ledgers survive independently and replay their daily counters.
func (s *Store) ReconcileRestore() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE nodes SET desired_revision=MAX(desired_revision+1,?),applied_revision=0,last_seen=0,online='{}',core_healthy=0", s.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE client_sessions SET revoked=1"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE device_config SET revision=MAX(revision+1,?)", s.Now().UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE removals SET immediate=1 WHERE status NOT IN ('revoked','cancelled')"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE nodes SET password='',password_expires=0 WHERE password_expires>0 AND password_expires<=?", s.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'backup.restored','controller')", s.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func secretError(err error) error {
	if err != nil {
		return fmt.Errorf("无法读取加密凭据，请检查数据库与主密钥是否配套: %w", err)
	}
	return nil
}

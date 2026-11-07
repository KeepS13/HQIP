// Package pgstore provides PostgreSQL connections for the existing transactional
// repositories while the offline importer retains SQLite support.
package pgstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var Schema string

var columnNames = map[string]map[string]bool{}

func init() {
	for _, stmt := range strings.Split(Schema, ";") {
		idx := strings.Index(stmt, "CREATE TABLE ")
		if idx < 0 {
			continue
		}
		s := stmt[idx+len("CREATE TABLE "):]
		i := strings.Index(s, "(")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(s[:i])
		cols := map[string]bool{}
		parts, _ := splitArgs(s[i+1 : strings.LastIndex(s, ")")])
		for _, p := range parts {
			f := strings.Fields(p)
			if len(f) > 1 && f[0] != "PRIMARY" && f[0] != "UNIQUE" {
				cols[strings.Trim(f[0], `"`)] = true
			}
		}
		columnNames[name] = cols
	}
}

func Config(dsn string) (*pgx.ConnConfig, error) {
	c, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("PostgreSQL 连接配置无效")
	}
	ip := net.ParseIP(c.Host)
	if ip == nil || !ip.IsLoopback() {
		if c.TLSConfig == nil || c.TLSConfig.InsecureSkipVerify {
			return nil, errors.New("远程 PostgreSQL 必须使用 sslmode=verify-full")
		}
		for _, f := range c.Fallbacks {
			if f.TLSConfig == nil || f.TLSConfig.InsecureSkipVerify {
				return nil, errors.New("PostgreSQL 不允许降级为未验证连接")
			}
		}
	}
	c.ConnectTimeout = 3 * time.Second
	c.RuntimeParams["statement_timeout"] = "10000"
	c.RuntimeParams["lock_timeout"] = "2000"
	c.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	c.RuntimeParams["application_name"] = "hqip-control"
	c.DefaultQueryExecMode = pgx.QueryExecModeExec
	return c, nil
}

func Open(dsn string) (*sql.DB, error) {
	c, err := Config(dsn)
	if err != nil {
		return nil, err
	}
	base := stdlib.GetConnector(*c)
	db := sql.OpenDB(connector{base})
	db.SetMaxOpenConns(24)
	db.SetMaxIdleConns(6)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("PostgreSQL 连接失败")
	}
	return db, nil
}

type connector struct{ driver.Connector }

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	v, e := c.Connector.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &conn{Conn: v}, nil
}

type conn struct{ driver.Conn }

func (c *conn) CheckNamedValue(v *driver.NamedValue) error {
	a, e := driver.DefaultParameterConverter.ConvertValue(v.Value)
	if e != nil {
		return e
	}
	if b, ok := a.(bool); ok {
		a = int64(0)
		if b {
			a = int64(1)
		}
	}
	v.Value = a
	return nil
}
func (c *conn) Begin() (driver.Tx, error) { return c.BeginTx(context.Background(), driver.TxOptions{}) }
func (c *conn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if o.Isolation == driver.IsolationLevel(sql.LevelDefault) {
		o.Isolation = driver.IsolationLevel(sql.LevelSerializable)
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}
func (c *conn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	s, e := translate(q, a)
	if e != nil {
		return nil, e
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, s, a)
}
func (c *conn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	s, e := translate(q, a)
	if e != nil {
		return nil, e
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, s, a)
	if err != nil {
		return nil, err
	}
	return &numericBoolRows{Rows: rows}, nil
}

type numericBoolRows struct{ driver.Rows }

func (r *numericBoolRows) Next(dest []driver.Value) error {
	if err := r.Rows.Next(dest); err != nil {
		return err
	}
	for i, v := range dest {
		if b, ok := v.(bool); ok {
			dest[i] = int64(0)
			if b {
				dest[i] = int64(1)
			}
		}
	}
	return nil
}
func (c *conn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }
func (c *conn) ResetSession(ctx context.Context) error {
	if v, ok := c.Conn.(driver.SessionResetter); ok {
		return v.ResetSession(ctx)
	}
	return nil
}
func (c *conn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// Prepare is intentionally unavailable: parameter types are bound at execution.
func (c *conn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("use parameterized Query/Exec instead of prepared dialect statements")
}

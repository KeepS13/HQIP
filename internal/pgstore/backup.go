package pgstore

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"
)

// pgTools uses the standard PostgreSQL archive format. Credentials travel in a
// private, temporary service file, never in argv, output, or child environment.
func pgTool(ctx context.Context, dsn, tool string, args ...string) error {
	c, err := Config(dsn)
	if err != nil {
		return err
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return errors.New("备份恢复需要 PostgreSQL URI 连接配置")
	}
	values := map[string]string{"host": c.Host, "port": strconv.Itoa(int(c.Port)), "dbname": c.Database, "user": c.User, "password": c.Password, "connect_timeout": "5", "application_name": "hqip-backup", "sslmode": "prefer"}
	for _, key := range []string{"sslmode", "sslrootcert", "sslcert", "sslkey", "sslcrl", "sslcrldir", "ssl_min_protocol_version", "ssl_max_protocol_version"} {
		if v := u.Query().Get(key); v != "" {
			values[key] = v
		}
	}
	var config strings.Builder
	config.WriteString("[hqip]\n")
	for key, value := range values {
		if strings.ContainsAny(value, "\r\n\x00") || strings.TrimSpace(value) != value {
			return errors.New("PostgreSQL 备份配置不能包含换行或空字节")
		}
		config.WriteString(key + "=" + value + "\n")
	}
	dir, err := os.MkdirTemp("", "hqip-pg-service-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	service := filepath.Join(dir, "service.conf")
	if err = os.WriteFile(service, []byte(config.String()), 0600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "PG") && key != "HQIP_DATABASE_URL" && key != "HQIP_REDIS_URL" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	// Debian's pg_wrapper can select an unrelated local cluster's old tool
	// version even when PGSERVICE is set. The explicit, non-secret target
	// disables local-cluster selection; authentication remains in service.conf.
	cmd.Env = append(cmd.Env, "PGSERVICEFILE="+service, "PGSERVICE=hqip", "PGHOST="+c.Host)
	if err = cmd.Run(); err != nil {
		return errors.New(tool + " 执行失败；请检查工具版本、连接权限与备份文件")
	}
	return nil
}

func Dump(ctx context.Context, dsn, destination string) error {
	c, err := Config(dsn)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*c)
	defer db.Close()
	var schema string
	if err = db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return err
	}
	if schema != "public" {
		return errors.New("正式备份只支持独立数据库的 public schema")
	}
	if _, err = os.Stat(destination); !os.IsNotExist(err) {
		return errors.New("备份文件必须不存在")
	}
	return pgTool(ctx, dsn, "pg_dump", "--no-password", "--schema=public", "--format=custom", "--no-owner", "--no-privileges", "--file="+destination)
}

func Restore(ctx context.Context, dsn, source string) error {
	c, err := Config(dsn)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*c)
	defer db.Close()
	var objects int
	if err = db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname!='information_schema')+
 (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname!='information_schema')`).Scan(&objects); err != nil {
		return err
	}
	if objects != 0 {
		return errors.New("PostgreSQL 恢复目标必须是全新空数据库，拒绝覆盖已有对象")
	}
	return pgTool(ctx, dsn, "pg_restore", "--no-password", "--dbname=service=hqip", "--single-transaction", "--exit-on-error", "--clean", "--if-exists", "--no-owner", "--no-privileges", "--no-comments", source)
}

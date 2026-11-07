package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strings"

	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/jackc/pgx/v5"
)

type MigrationTable struct {
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}
type MigrationReport struct {
	Tables       []MigrationTable `json:"tables"`
	MasterSHA256 string           `json:"masterSHA256"`
}

// MigratePostgres reads an immutable SQLite snapshot; the target schema and
// vault directory must be empty. No source file is modified or removed.
func MigratePostgres(ctx context.Context, sourceDir, targetDir, dsn string) (MigrationReport, error) {
	var report MigrationReport
	key, err := os.ReadFile(filepath.Join(sourceDir, "master.key"))
	if err != nil || len(key) != 32 {
		return report, errors.New("迁移需要快照数据库及匹配的 32 字节 master.key")
	}
	keyHash := sha256.Sum256(key)
	report.MasterSHA256 = hex.EncodeToString(keyHash[:])
	database, err := filepath.Abs(filepath.Join(sourceDir, "hqip.db"))
	if err != nil {
		return report, err
	}
	if _, err = os.Stat(database); err != nil {
		return report, err
	}
	source, err := sql.Open("sqlite", "file:"+filepath.ToSlash(database)+"?mode=ro")
	if err != nil {
		return report, err
	}
	defer source.Close()
	source.SetMaxOpenConns(1)
	snapshot, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, err
	}
	defer snapshot.Rollback()
	var integrity, version string
	if err = snapshot.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return report, errors.New("SQLite 完整性检查失败")
	}
	if err = snapshot.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='schema_version'").Scan(&version); err != nil || version != "5" {
		return report, errors.New("请先将 SQLite 快照升级到当前 schema 5")
	}
	if err = snapshot.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='payments_schema_version'").Scan(&version); err != nil || version != "1" {
		return report, errors.New("请先将 SQLite 数据库升级到当前支付版本后重新制作快照")
	}
	var brokenReferences int
	if err = snapshot.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&brokenReferences); err != nil || brokenReferences != 0 {
		return report, errors.New("SQLite 外键检查失败")
	}
	vault, err := secure.Open(filepath.Join(sourceDir, "master.key"))
	if err != nil {
		return report, err
	}
	if err = validateVault(ctx, snapshot, vault); err != nil {
		return report, err
	}
	cfg, err := pgstore.Config(dsn)
	if err != nil {
		return report, err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return report, errors.New("PostgreSQL 连接失败")
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		return report, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(719022634)"); err != nil {
		return report, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&count); err != nil {
		return report, err
	}
	if count != 0 {
		return report, errors.New("PostgreSQL 目标 schema 非空，拒绝覆盖")
	}
	// Reserve only after the destination has been validated. On any failure the
	// owned directory contains useful diagnostics; it is never silently reused.
	if err = os.Mkdir(targetDir, 0700); err != nil {
		return report, errors.New("目标密钥目录必须不存在")
	}
	if err = os.WriteFile(filepath.Join(targetDir, "master.key"), key, 0600); err != nil {
		return report, err
	}
	if err = os.WriteFile(filepath.Join(targetDir, "postgresql.enabled"), []byte("1\n"), 0600); err != nil {
		return report, err
	}
	// pgx extended protocol parses one statement at a time; split the static
	// schema script before executing it in the migration transaction.
	for _, statement := range strings.Split(pgstore.Schema+";"+postgresPaymentSchema(), ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			if _, err = tx.Exec(ctx, statement); err != nil {
				return report, err
			}
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM metadata"); err != nil {
		return report, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM payment_settings"); err != nil {
		return report, err
	}
	catalog, err := snapshot.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY rowid")
	if err != nil {
		return report, err
	}
	var tables []string
	for catalog.Next() {
		var name string
		if err = catalog.Scan(&name); err != nil {
			catalog.Close()
			return report, err
		}
		tables = append(tables, name)
	}
	err = catalog.Err()
	catalog.Close()
	if err != nil {
		return report, err
	}
	for _, table := range tables {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=$1)", table).Scan(&exists); err != nil {
			return report, err
		}
		if !exists {
			if err = createRetiredPostgresTable(ctx, snapshot, tx, table); err != nil {
				return report, err
			}
		}
		entry, e := copyPostgresTable(ctx, snapshot, tx, table)
		if e != nil {
			return report, fmt.Errorf("迁移表 %s: %w", table, e)
		}
		report.Tables = append(report.Tables, entry)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO metadata(key,value) VALUES('postgres_schema_version','1'),('master_key_sha256',$1)", report.MasterSHA256); err != nil {
		return report, err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	reportPath := filepath.Join(targetDir, "migration-report.json")
	tmpReport := reportPath + ".tmp"
	if err = os.WriteFile(tmpReport, data, 0600); err != nil {
		return report, err
	}
	if err = tx.Commit(ctx); err != nil {
		_ = os.Remove(tmpReport)
		return report, err
	}
	if err = os.Rename(tmpReport, reportPath); err != nil {
		_ = os.Remove(tmpReport)
		return report, fmt.Errorf("迁移已提交但报告写入失败: %w", err)
	}
	return report, nil
}

// Retired notification tables are retained as read-compatible archives. Their
// old DDL is data only and is never executed. Unknown tables still fail closed.
func createRetiredPostgresTable(ctx context.Context, source *sql.Tx, target pgx.Tx, table string) error {
	if table != "feishu_settings" && table != "notifications" {
		return fmt.Errorf("源库包含待处理的历史表 %s；拒绝静默丢失数据", table)
	}
	rows, err := source.QueryContext(ctx, "PRAGMA table_info("+quotedIdentifier(table)+")")
	if err != nil {
		return err
	}
	columns := []string{"rowid BIGINT GENERATED BY DEFAULT AS IDENTITY UNIQUE"}
	for rows.Next() {
		var ordinal, required, primary int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&ordinal, &name, &kind, &required, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		switch k := strings.ToUpper(kind); {
		case strings.Contains(k, "INT"):
			kind = "BIGINT"
		case strings.Contains(k, "BLOB"):
			kind = "BYTEA"
		case strings.Contains(k, "REAL") || strings.Contains(k, "FLOA") || strings.Contains(k, "DOUB"):
			kind = "DOUBLE PRECISION"
		default:
			kind = "TEXT"
		}
		columns = append(columns, quotedIdentifier(name)+" "+kind)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var ddl string
	if err = source.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl); err != nil {
		return err
	}
	if _, err = target.Exec(ctx, "CREATE TABLE "+quotedIdentifier(table)+"("+strings.Join(columns, ",")+")"); err != nil {
		return err
	}
	if _, err = target.Exec(ctx, "CREATE TABLE IF NOT EXISTS hqip_legacy_schema(name text PRIMARY KEY,sqlite_ddl text NOT NULL)"); err != nil {
		return err
	}
	_, err = target.Exec(ctx, "INSERT INTO hqip_legacy_schema VALUES($1,$2)", table, ddl)
	return err
}

func quotedIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func hashMigrationRow(h hash.Hash, values []any) error {
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	fmt.Fprintf(h, "%d:", len(b))
	h.Write(b)
	return nil
}

func copyPostgresTable(ctx context.Context, source *sql.Tx, target pgx.Tx, table string) (MigrationTable, error) {
	entry := MigrationTable{Name: table}
	rows, err := source.QueryContext(ctx, "SELECT rowid,* FROM "+quotedIdentifier(table)+" ORDER BY rowid")
	if err != nil {
		return entry, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return entry, err
	}
	columns[0] = "rowid"
	sourceHash := sha256.New()
	batch := make([][]any, 0, 500)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, e := target.CopyFrom(ctx, pgx.Identifier{table}, columns, pgx.CopyFromRows(batch))
		batch = batch[:0]
		return e
	}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return entry, err
		}
		if err = hashMigrationRow(sourceHash, values); err != nil {
			return entry, err
		}
		batch = append(batch, values)
		entry.Rows++
		if len(batch) == 500 {
			if err = flush(); err != nil {
				return entry, err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return entry, err
	}
	rows.Close()
	if err = flush(); err != nil {
		return entry, err
	}
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = quotedIdentifier(col)
	}
	check, err := target.Query(ctx, "SELECT "+strings.Join(quoted, ",")+" FROM "+quotedIdentifier(table)+" ORDER BY rowid")
	if err != nil {
		return entry, err
	}
	targetHash := sha256.New()
	var n int64
	for check.Next() {
		values, e := check.Values()
		if e != nil {
			check.Close()
			return entry, e
		}
		if e = hashMigrationRow(targetHash, values); e != nil {
			check.Close()
			return entry, e
		}
		n++
	}
	err = check.Err()
	check.Close()
	if err != nil {
		return entry, err
	}
	entry.SHA256 = hex.EncodeToString(sourceHash.Sum(nil))
	if n != entry.Rows || entry.SHA256 != hex.EncodeToString(targetHash.Sum(nil)) {
		return entry, errors.New("行数或逐行摘要不一致")
	}
	for _, col := range columns {
		var seq *string
		if err = target.QueryRow(ctx, "SELECT pg_get_serial_sequence($1,$2)", quotedIdentifier(table), col).Scan(&seq); err != nil {
			return entry, err
		}
		if seq != nil {
			var max int64
			if err = target.QueryRow(ctx, "SELECT COALESCE(MAX("+quotedIdentifier(col)+"),0) FROM "+quotedIdentifier(table)).Scan(&max); err != nil {
				return entry, err
			}
			value := max
			if value == 0 {
				value = 1
			}
			if _, err = target.Exec(ctx, "SELECT setval($1::regclass,$2,$3)", *seq, value, max > 0); err != nil {
				return entry, err
			}
		}
	}
	return entry, nil
}

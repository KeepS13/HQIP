package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/jackc/pgx/v5/stdlib"
)

// InitializePostgres is an explicit operation, never an automatic overwrite.
// Existing schemas must be migrated by a versioned migration, not bootstrapped.
func InitializePostgres(ctx context.Context, dsn string) error {
	c, err := pgstore.Config(dsn)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*c)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(719022634)"); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("PostgreSQL 目标 schema 必须为空，拒绝覆盖已有数据")
	}
	// PostgreSQL extended protocol accepts one statement per Parse. Execute
	// schema statements individually instead of sending the whole migration
	// script as one multi-command query.
	for _, statement := range strings.Split(pgstore.Schema+";"+postgresPaymentSchema()+";"+postgresAllocationSchema()+";"+postgresOperationsSchema()+";"+postgresOperationLogsSchema()+";"+postgresOfflineSchema(), ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('controller_id',$1)", NewID()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('subscription_allocations_version','1')"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('operations_schema_version','1')"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('operation_logs_version','1')"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('offline_admission_version','1')"); err != nil {
		return err
	}
	return tx.Commit()
}

// OpenPostgres requires the existing paired vault key. Migration copies the key
// unchanged; opening a remote database never silently creates a different key.
func OpenPostgres(dataDir, baseURL, dsn string) (*Store, error) {
	if _, err := os.Stat(filepath.Join(dataDir, "master.key")); err != nil {
		return nil, errors.New("PostgreSQL 必须提供与数据匹配的 master.key")
	}
	vault, err := secure.Open(filepath.Join(dataDir, "master.key"))
	if err != nil {
		return nil, err
	}
	db, err := pgstore.Open(dsn)
	if err != nil {
		return nil, err
	}
	var version string
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='postgres_schema_version'").Scan(&version); err != nil || version != "1" {
		db.Close()
		return nil, errors.New("PostgreSQL schema 未初始化或版本不兼容")
	}
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='payments_schema_version'").Scan(&version); err != nil || version != "1" {
		db.Close()
		return nil, errors.New("支付 schema 未升级，请使用迁移账号执行 hqip migrate-payments")
	}
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='subscription_allocations_version'").Scan(&version); err != nil || version != "1" {
		db.Close()
		return nil, errors.New("订阅节点分配尚未升级，请使用迁移账号执行 hqip migrate-subscription-allocations")
	}
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='operations_schema_version'").Scan(&version); err != nil || version != "1" {
		db.Close()
		return nil, errors.New("服务器运维 schema 未升级，请使用迁移账号执行 hqip migrate-operations")
	}
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='operation_logs_version'").Scan(&version); err != nil || version != "1" {
		db.Close()
		return nil, errors.New("操作日志 schema 未升级，请使用迁移账号执行 hqip migrate-operation-logs")
	}
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='offline_admission_version'").Scan(&version); err != nil || version != "1" {
		db.Close()
		return nil, errors.New("离线授权 schema 未升级，请执行 hqip migrate-offline-admission")
	}
	key, err := os.ReadFile(filepath.Join(dataDir, "master.key"))
	if err != nil {
		db.Close()
		return nil, err
	}
	h := sha256.Sum256(key)
	fingerprint := hex.EncodeToString(h[:])
	var known int
	if err = db.QueryRow("SELECT count(*) FROM metadata WHERE key='master_key_sha256'").Scan(&known); err != nil {
		db.Close()
		return nil, err
	}
	if known == 0 {
		if err = validateVault(context.Background(), db, vault); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO metadata(key,value) VALUES('master_key_sha256',?)", fingerprint); err != nil {
		db.Close()
		return nil, err
	}
	var expected string
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='master_key_sha256'").Scan(&expected); err != nil || expected != fingerprint {
		db.Close()
		return nil, errors.New("PostgreSQL 数据库与 master.key 不匹配")
	}
	if err = os.WriteFile(filepath.Join(dataDir, "postgresql.enabled"), []byte("1\n"), 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, Vault: vault, BaseURL: strings.TrimRight(baseURL, "/"), DataDir: dataDir, Now: time.Now, postgresDSN: dsn}, nil
}

func OpenConfigured(dataDir, baseURL, dsn string) (*Store, error) {
	if dsn != "" {
		return OpenPostgres(dataDir, baseURL, dsn)
	}
	return Open(dataDir, baseURL)
}

func (s *Store) Postgres() bool { return s.postgresDSN != "" }

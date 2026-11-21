package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/internal/store"
)

// storage-info is a local deployment probe, never an HTTP endpoint. It emits
// schema capabilities and a key fingerprint without database URLs or secrets.
func storageInfo(args []string) error {
	f := flag.NewFlagSet("storage-info", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip", "配套数据目录")
	if err := f.Parse(args); err != nil {
		return err
	}
	s, err := store.OpenConfigured(*dir, "", os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer s.Close()
	var schema string
	if err = s.DB.QueryRow("SELECT value FROM metadata WHERE key='schema_version'").Scan(&schema); err != nil {
		return err
	}
	version, err := strconv.Atoi(schema)
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(*dir, "master.key"))
	if err != nil {
		return err
	}
	h := sha256.Sum256(key)
	backend, pgVersion := "sqlite", 0
	if s.Postgres() {
		backend, pgVersion = "postgresql", 1
	}
	var paymentOrders, paymentEnabled int64
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM payment_orders").Scan(&paymentOrders); err != nil {
		return err
	}
	if err = s.DB.QueryRow("SELECT enabled FROM payment_settings WHERE id=1").Scan(&paymentEnabled); err != nil {
		return err
	}
	var realityVersion int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM metadata WHERE key='vless_reality_version' AND value='1'").Scan(&realityVersion); err != nil {
		return err
	}
	var nodeQuotaVersion int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM metadata WHERE key='node_quota_version' AND value='1'").Scan(&nodeQuotaVersion); err != nil {
		return err
	}
	var connectionSlotsVersion int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM metadata WHERE key='connection_slots_version' AND value='1'").Scan(&connectionSlotsVersion); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"connectionSlotsVersion": connectionSlotsVersion, "nodeQuotaVersion": nodeQuotaVersion, "backend": backend, "schemaVersion": version, "postgresSchemaVersion": pgVersion, "serverBillingVersion": 2, "paymentsVersion": 1, "subscriptionAllocationsVersion": 1, "subscriptionRenewalsVersion": 1, "operationsVersion": 1, "operationLogsVersion": 1, "offlineAdmissionVersion": 1, "trafficBillingVersion": 1, "vlessRealityVersion": realityVersion, "paymentsRequired": paymentOrders > 0 || paymentEnabled != 0, "masterSHA256": hex.EncodeToString(h[:])})
}

func migrateSubscriptionRenewals(args []string) error {
	if len(args) != 0 {
		return errors.New("migrate-subscription-renewals 不接受额外参数")
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("请使用 PostgreSQL 迁移账号配置 HQIP_DATABASE_URL；SQLite 随启动升级")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return store.MigratePostgresSubscriptionRenewals(ctx, dsn)
}

func migrateOperationLogs(args []string) error {
	if len(args) != 0 {
		return errors.New("migrate-operation-logs 不接受额外参数")
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("请配置 PostgreSQL 迁移账号；SQLite 随启动升级")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return store.MigratePostgresOperationLogs(ctx, dsn)
}

func migrateOperations(args []string) error {
	if len(args) != 0 {
		return errors.New("migrate-operations 不接受额外参数")
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("请配置 PostgreSQL 迁移账号；SQLite 随启动升级")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return store.MigratePostgresOperations(ctx, dsn)
}

func migrateSubscriptionAllocations(args []string) error {
	f := flag.NewFlagSet("migrate-subscription-allocations", flag.ContinueOnError)
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("migrate-subscription-allocations 不接受额外参数")
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("请使用 PostgreSQL 迁移账号配置 HQIP_DATABASE_URL；SQLite 随启动升级")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return store.MigratePostgresSubscriptionAllocations(ctx, dsn)
}

func migratePayments(args []string) error {
	f := flag.NewFlagSet("migrate-payments", flag.ContinueOnError)
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("migrate-payments 不接受额外参数")
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("请使用 PostgreSQL 迁移账号配置 HQIP_DATABASE_URL；SQLite 随启动升级")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return store.MigratePostgresPayments(ctx, dsn)
}

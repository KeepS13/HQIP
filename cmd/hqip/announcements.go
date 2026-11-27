package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/KeepS13/HQIP/internal/store"
)

// Explicit PostgreSQL migration keeps DDL credentials out of the HTTP process.
func migrateAnnouncements(args []string) error {
	if len(args) != 0 {
		return errors.New("migrate-announcements 不接受额外参数")
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("请配置 PostgreSQL 迁移账号；SQLite 随启动升级")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return store.MigratePostgresAnnouncements(ctx, dsn)
}

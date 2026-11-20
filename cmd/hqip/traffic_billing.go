package main

import (
	"context"
	"errors"
	"flag"
	"github.com/KeepS13/HQIP/internal/store"
	"os"
	"time"
)

func migrateTrafficBilling(args []string) error {
	flags := flag.NewFlagSet("migrate-traffic-billing", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("迁移需要 HQIP_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return store.MigratePostgresTrafficBilling(ctx, dsn)
}

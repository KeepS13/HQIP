package main

import (
	"context"
	"errors"
	"flag"
	"github.com/KeepS13/HQIP/internal/store"
	"os"
	"time"
)

func migrateOfflineAdmission(args []string) error {
	f := flag.NewFlagSet("migrate-offline-admission", flag.ContinueOnError)
	if err := f.Parse(args); err != nil {
		return err
	}
	dsn := os.Getenv("HQIP_DATABASE_URL")
	if dsn == "" {
		return errors.New("迁移需要 HQIP_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return store.MigratePostgresOffline(ctx, dsn)
}

func offlineAdmissionSettings(args []string) error {
	f := flag.NewFlagSet("offline-admission", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip-control", "数据目录")
	node := f.String("node", "", "节点 ID")
	enabled := f.Bool("enabled", false, "允许发放新的离线许可；关闭不删除未结算许可")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *node == "" {
		return errors.New("需要 --node")
	}
	s, err := store.OpenConfigured(*dir, "https://hqip.sharevps.online", os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer s.Close()
	return s.SetOfflineAdmissionEnabled(*node, *enabled)
}

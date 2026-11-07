package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/KeepS13/HQIP/internal/store"
)

func main() {
	source := flag.String("snapshot-dir", "", "已停止写入的 SQLite 快照目录")
	target := flag.String("target-data-dir", "", "新的 PostgreSQL 配套密钥目录（必须不存在）")
	flag.Parse()
	if *source == "" || *target == "" || os.Getenv("HQIP_DATABASE_URL") == "" {
		log.Fatal("需要 --snapshot-dir、--target-data-dir 和 HQIP_DATABASE_URL 环境变量")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 30*time.Minute)
	defer timeout()
	report, err := store.MigratePostgres(ctx, *source, *target, os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	json.NewEncoder(os.Stdout).Encode(report)
}

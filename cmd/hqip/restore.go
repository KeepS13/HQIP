package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/store"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

func restore(args []string) error {
	f := flag.NewFlagSet("restore", flag.ContinueOnError)
	dir := f.String("data-dir", "", "恢复目标目录（必须为空）")
	input := f.String("input", "", "备份文件路径")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *input == "" {
		return errors.New("请指定 --data-dir 空目录与 --input 备份文件")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(abs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) > 0 {
		return errors.New("恢复目标目录必须为空；请先停止服务并保留原数据目录")
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(abs), "hqip-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	file, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
		if h.Name != "hqip.db" && h.Name != "hqip.pg.dump" && h.Name != "master.key" && h.Name != "manifest.json" {
			return errors.New("备份包含不允许的文件")
		}
		if seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 4<<30 {
			return errors.New("备份文件格式无效")
		}
		seen[h.Name] = true
		out, e := os.OpenFile(filepath.Join(temp, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, io.LimitReader(tr, h.Size))
		out.Close()
		if e != nil {
			return e
		}
	}
	if seen["hqip.db"] == seen["hqip.pg.dump"] || !seen["master.key"] {
		return errors.New("备份缺少数据库或主密钥")
	}
	if seen["manifest.json"] {
		data, e := os.ReadFile(filepath.Join(temp, "manifest.json"))
		var manifest struct {
			SubscriptionRenewalsVersion int `json:"subscriptionRenewalsVersion"`
		}
		if e != nil || json.Unmarshal(data, &manifest) != nil || manifest.SubscriptionRenewalsVersion < 0 || manifest.SubscriptionRenewalsVersion > 2 {
			return errors.New("备份的订阅周期版本不兼容")
		}
	}
	postgres := seen["hqip.pg.dump"]
	if postgres != (os.Getenv("HQIP_DATABASE_URL") != "") {
		return errors.New("备份类型与 HQIP_DATABASE_URL 配置不匹配")
	}
	if postgres {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err = pgstore.Restore(ctx, os.Getenv("HQIP_DATABASE_URL"), filepath.Join(temp, "hqip.pg.dump")); err != nil {
			return err
		}
		// Disaster recovery already runs with the migration identity against an
		// empty target. Upgrade this additive capability before runtime checks so
		// pre-operation-log backups remain recoverable by the current release.
		if err = store.MigratePostgresOperationLogs(ctx, os.Getenv("HQIP_DATABASE_URL")); err != nil {
			return err
		}
		if err = store.MigratePostgresSubscriptionRenewals(ctx, os.Getenv("HQIP_DATABASE_URL")); err != nil {
			return err
		}
		if err = store.MigratePostgresAnnouncements(ctx, os.Getenv("HQIP_DATABASE_URL")); err != nil {
			return err
		}
	}
	db, err := store.OpenConfigured(temp, "", os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		return err
	}
	err = db.CheckIntegrity()
	if err == nil {
		err = db.ReconcileRestore()
	}
	db.Close()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return err
	}
	dataFile := "hqip.db"
	if postgres {
		dataFile = "postgresql.enabled"
	}
	for _, name := range []string{"master.key", dataFile} {
		if err = os.Rename(filepath.Join(temp, name), filepath.Join(abs, name)); err != nil {
			return err
		}
	}
	log.Printf("已恢复并验证数据库与主密钥：%s", abs)
	return nil
}

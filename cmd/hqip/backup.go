package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/KeepS13/HQIP/internal/store"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

func backup(args []string) error {
	f := flag.NewFlagSet("backup", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip", "数据目录")
	output := f.String("output", "", "备份文件路径 (.tar.gz)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("请指定 --output 备份文件")
	}
	if os.Getenv("HQIP_DATABASE_URL") == "" {
		if _, err := os.Stat(filepath.Join(*dir, "hqip.db")); err != nil {
			return err
		}
	}
	db, err := store.OpenConfigured(*dir, "", os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.CheckIntegrity(); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(*dir, "backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	databaseFile, backend := "hqip.db", "sqlite"
	if db.Postgres() {
		databaseFile, backend = "hqip.pg.dump", "postgresql"
	}
	if err = db.Snapshot(ctx, filepath.Join(temp, databaseFile)); err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(*dir, "master.key"))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(temp, "master.key"), key, 0600); err != nil {
		return err
	}
	manifest, _ := json.Marshal(map[string]any{"version": 1, "backend": backend, "createdAt": time.Now().UTC(), "containsSecrets": true, "subscriptionRenewalsVersion": 2})
	if err = os.WriteFile(filepath.Join(temp, "manifest.json"), manifest, 0600); err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		file.Close()
		if !success {
			os.Remove(*output)
		}
	}()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, name := range []string{databaseFile, "master.key", "manifest.json"} {
		path := filepath.Join(temp, name)
		info, e := os.Stat(path)
		if e != nil {
			return e
		}
		header := &tar.Header{Name: name, Mode: 0600, Size: info.Size(), ModTime: info.ModTime()}
		if err = tw.WriteHeader(header); err != nil {
			return err
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	success = true
	log.Printf("已创建一致性备份 %s（包含主密钥，请妥善保存）", *output)
	return nil
}

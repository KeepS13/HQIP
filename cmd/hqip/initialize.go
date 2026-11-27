package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/KeepS13/HQIP/internal/rules"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
	"log"
	"os"
	"path/filepath"
)

func initAdmin(args []string) error {
	f := flag.NewFlagSet("init-admin", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip", "数据目录")
	username := f.String("username", "admin", "管理员用户名")
	output := f.String("output", "", "初始凭据输出文件（0600，不会覆盖）")
	rulesFile := f.String("rules", "", "可选：在空站初始化时导入无凭据规则包")
	initializePostgres := f.Bool("initialize-postgres", false, "显式初始化空 PostgreSQL schema（仅限全新站点）")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("请指定 --output 凭据文件，密码不会打印到日志")
	}
	var initialRules *rules.Bundle
	if *rulesFile != "" {
		raw, err := os.ReadFile(*rulesFile)
		if err != nil {
			return err
		}
		var bundle rules.Bundle
		if len(raw) > 8<<20 {
			return errors.New("规则文件不能超过 8 MiB")
		}
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return err
		}
		bundle, err = rules.Normalize(bundle)
		if err != nil {
			return err
		}
		initialRules = &bundle
	}
	if *initializePostgres {
		if os.Getenv("HQIP_DATABASE_URL") == "" {
			return errors.New("初始化 PostgreSQL 需要 HQIP_DATABASE_URL")
		}
		if _, err := os.Stat(filepath.Join(*dir, "hqip.db")); err == nil {
			return errors.New("已有 SQLite 数据，必须通过离线迁移导入，不能初始化空站")
		}
		if err := store.InitializePostgres(context.Background(), os.Getenv("HQIP_DATABASE_URL")); err != nil {
			return err
		}
		if err := os.MkdirAll(*dir, 0700); err != nil {
			return err
		}
		if _, err := secure.Open(filepath.Join(*dir, "master.key")); err != nil {
			return err
		}
	}
	db, err := store.OpenConfigured(*dir, "", os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	if initialRules != nil {
		if _, _, _, err := db.Admin(); err == nil {
			return errors.New("已有管理员，不能通过初始化命令修改规则")
		}
		if _, err := db.SaveRules(*initialRules); err != nil {
			return err
		}
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
	password := secure.Token()
	if err = json.NewEncoder(file).Encode(map[string]string{"username": *username, "password": password}); err != nil {
		return fmt.Errorf("初始凭据写入失败：%w", err)
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = db.InitAdmin(*username, password); err != nil {
		return err
	}
	success = true
	log.Printf("管理员已初始化，初始凭据保存到 %s；首次登录必须修改密码", *output)
	return nil
}

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/KeepS13/HQIP/internal/control"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/ratelimit"
	"github.com/KeepS13/HQIP/internal/rules"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：hqip serve | init-admin | backup | restore")
	}
	switch args[0] {
	case "migrate-subscription-renewals":
		return migrateSubscriptionRenewals(args[1:])
	case "migrate-traffic-billing":
		return migrateTrafficBilling(args[1:])
	case "migrate-offline-admission":
		return migrateOfflineAdmission(args[1:])
	case "offline-admission":
		return offlineAdmissionSettings(args[1:])
	case "serve":
		return serve(args[1:])
	case "init-admin":
		return initAdmin(args[1:])
	case "backup":
		return backup(args[1:])
	case "restore":
		return restore(args[1:])
	case "storage-info":
		return storageInfo(args[1:])
	case "migrate-payments":
		return migratePayments(args[1:])
	case "migrate-subscription-allocations":
		return migrateSubscriptionAllocations(args[1:])
	case "migrate-operations":
		return migrateOperations(args[1:])
	case "migrate-operation-logs":
		return migrateOperationLogs(args[1:])
	default:
		return errors.New("未知命令；可用命令：serve、init-admin、backup、restore、storage-info、migrate-payments、migrate-subscription-allocations")
	}
}
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip", "数据目录")
	listen := f.String("listen", "127.0.0.1:8080", "监听地址")
	publicListen := f.String("public-listen", "127.0.0.1:8081", "仅客户端及 Agent API 监听地址")
	adminOrigin := f.String("admin-origin", "http://127.0.0.1:5173", "管理员页面根地址（HTTPS 或本地 HTTP）")
	public := f.String("public-url", "", "API 公网 HTTPS 地址")
	userOrigin := f.String("user-origin", "http://127.0.0.1:5174", "用户页面根地址")
	artifacts := f.String("artifact-dir", "/opt/hqip/artifacts", "节点安装文件目录")
	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := url.Parse(*public)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("--public-url 必须为网站根地址，例如 https://testvps.sharevps.online")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return errors.New("公网网站必须使用 HTTPS")
	}
	if err := validateAdminOrigin(*adminOrigin); err != nil {
		return err
	}
	if err := validateAdminOrigin(*userOrigin); err != nil {
		return err
	}
	for _, address := range []string{*listen, *publicListen} {
		host, _, e := net.SplitHostPort(address)
		if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("两个监听地址必须使用回环 IP，公网访问需经可信 HTTPS 反向代理")
		}
	}
	if os.Getenv("HQIP_DATABASE_URL") != "" && os.Getenv("HQIP_REDIS_URL") == "" {
		return errors.New("PostgreSQL 模式必须配置 HQIP_REDIS_URL，共享认证限流不能退回进程内计数")
	}
	db, err := store.OpenConfigured(*dir, strings.TrimRight(*public, "/"), os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, _, _, err = db.Admin(); err != nil {
		return errors.New("请先运行 hqip init-admin 初始化管理员")
	}
	if _, err = db.Rules(); err != nil {
		return err
	}
	var limiter control.LoginLimiter
	if address := os.Getenv("HQIP_REDIS_URL"); address != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		namespace := os.Getenv("HQIP_REDIS_NAMESPACE")
		if namespace == "" {
			namespace = "hqip"
		}
		shared, openErr := ratelimit.Open(ctx, address, namespace)
		cancel()
		if openErr != nil {
			return openErr
		}
		defer shared.Close()
		limiter = shared
	}
	s := control.New(db, control.Options{LoginLimiter: limiter, PublicURL: strings.TrimRight(*public, "/"), AdminOrigin: strings.TrimRight(*adminOrigin, "/"), UserOrigin: strings.TrimRight(*userOrigin, "/"), ArtifactDir: *artifacts})
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	adminListener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer adminListener.Close()
	publicListener, err := net.Listen("tcp", *publicListen)
	if err != nil {
		return err
	}
	defer publicListener.Close()
	server := &http.Server{Addr: *listen, Handler: s.AdminHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	publicServer := &http.Server{Addr: *publicListen, Handler: s.PublicHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	errCh := make(chan error, 4)
	backgroundDone := make(chan struct{})
	go func() { defer close(backgroundDone); errCh <- runBackground(ctx, db, s) }()
	defer func() { cancel(); <-backgroundDone }()
	go func() {
		log.Printf("HQIP 本地管理服务正在监听 %s", *listen)
		errCh <- server.Serve(adminListener)
	}()
	go func() {
		log.Printf("HQIP 客户端及 Agent API 正在监听 %s", *publicListen)
		errCh <- publicServer.Serve(publicListener)
	}()
	select {
	case <-ctx.Done():
	case err = <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
			_ = server.Close()
			_ = publicServer.Close()
			return err
		}
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	adminErr := server.Shutdown(shutdown)
	publicErr := publicServer.Shutdown(shutdown)
	return errors.Join(adminErr, publicErr)
}
func validateAdminOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return errors.New("--admin-origin 必须为不含凭据、路径和查询参数的根地址")
	}
	if u.Scheme == "https" || (u.Scheme == "http" && u.Hostname() == "127.0.0.1") {
		return nil
	}
	return errors.New("--admin-origin 必须使用 HTTPS，或本地 http://127.0.0.1:端口")
}
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
	manifest, _ := json.Marshal(map[string]any{"version": 1, "backend": backend, "createdAt": time.Now().UTC(), "containsSecrets": true, "subscriptionRenewalsVersion": 1})
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
		if e != nil || json.Unmarshal(data, &manifest) != nil || manifest.SubscriptionRenewalsVersion < 0 || manifest.SubscriptionRenewalsVersion > 1 {
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

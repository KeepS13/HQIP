package main

import (
	"context"
	"errors"
	"flag"
	"github.com/KeepS13/HQIP/internal/control"
	"github.com/KeepS13/HQIP/internal/ratelimit"
	"github.com/KeepS13/HQIP/internal/store"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

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

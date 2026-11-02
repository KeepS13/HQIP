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
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
	"github.com/KeepS13/HQIP/web"
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
	case "serve":
		return serve(args[1:])
	case "init-admin":
		return initAdmin(args[1:])
	case "backup":
		return backup(args[1:])
	case "restore":
		return restore(args[1:])
	default:
		return errors.New("未知命令；可用命令：serve、init-admin、backup、restore")
	}
}
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip", "数据目录")
	listen := f.String("listen", "127.0.0.1:8080", "监听地址")
	publicListen := f.String("public-listen", "127.0.0.1:8081", "仅客户端及 Agent API 监听地址")
	adminOrigin := f.String("admin-origin", "http://127.0.0.1:8080", "本地管理员页面地址")
	public := f.String("public-url", "", "公网 HTTPS 地址")
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
	adminURL, e := url.Parse(*adminOrigin)
	if e != nil || adminURL.User != nil || adminURL.RawQuery != "" || adminURL.Fragment != "" || (adminURL.Path != "" && adminURL.Path != "/") || adminURL.Scheme != "http" || adminURL.Hostname() != "127.0.0.1" {
		return errors.New("--admin-origin 必须为本地 http://127.0.0.1:端口")
	}
	for _, address := range []string{*listen, *publicListen} {
		host, _, e := net.SplitHostPort(address)
		if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("两个监听地址必须使用回环 IP，公网访问只能通过 SSH 隧道")
		}
	}
	db, err := store.Open(*dir, strings.TrimRight(*public, "/"))
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
	s := control.New(db, control.Options{PublicURL: strings.TrimRight(*public, "/"), AdminOrigin: strings.TrimRight(*adminOrigin, "/"), ArtifactDir: *artifacts, Assets: web.Assets()})
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
	server := &http.Server{Addr: *listen, Handler: s.AdminHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	publicServer := &http.Server{Addr: *publicListen, Handler: s.PublicHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	errCh := make(chan error, 3)
	go func() { errCh <- s.RunWorker(ctx) }()
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
func initAdmin(args []string) error {
	f := flag.NewFlagSet("init-admin", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip", "数据目录")
	username := f.String("username", "admin", "管理员用户名")
	output := f.String("output", "", "初始凭据输出文件（0600，不会覆盖）")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("请指定 --output 凭据文件，密码不会打印到日志")
	}
	db, err := store.Open(*dir, "")
	if err != nil {
		return err
	}
	defer db.Close()
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
	if _, err := os.Stat(filepath.Join(*dir, "hqip.db")); err != nil {
		return err
	}
	db, err := store.Open(*dir, "")
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err = db.Snapshot(ctx, filepath.Join(temp, "hqip.db")); err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(*dir, "master.key"))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(temp, "master.key"), key, 0600); err != nil {
		return err
	}
	manifest, _ := json.Marshal(map[string]any{"version": 1, "createdAt": time.Now().UTC(), "containsSecrets": true})
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
	for _, name := range []string{"hqip.db", "master.key", "manifest.json"} {
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
		if h.Name != "hqip.db" && h.Name != "master.key" && h.Name != "manifest.json" {
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
	if !seen["hqip.db"] || !seen["master.key"] {
		return errors.New("备份缺少数据库或主密钥")
	}
	db, err := store.Open(temp, "")
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
	for _, name := range []string{"master.key", "hqip.db"} {
		if err = os.Rename(filepath.Join(temp, name), filepath.Join(abs, name)); err != nil {
			return err
		}
	}
	log.Printf("已恢复并验证数据库与主密钥：%s", abs)
	return nil
}

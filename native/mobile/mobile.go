//go:build android

// Package mobile binds the existing controller to Android's Keystore, VPN and
// browser. JNI exchanges secret-free JSON views; credentials remain encrypted
// in the app's private directory and never enter the Godot scene tree.
package mobile

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sync"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/clientbridge"
	"github.com/KeepS13/HQIP/internal/clientrelease"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/experimental/libbox"
)

// Callbacks run on Go worker threads. UI calls must marshal to Android's main
// thread. Protect binds to a physical network; OpenTun requires prior consent.
type Callbacks interface {
	Protect(int32) error
	OpenTun(string) (int32, error)
	CloseTun() error
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
	OpenURL(string) error
	Interfaces() (string, error)
	Lookup(string) (string, error)
	InstallAPK(string) error
}

type Runtime struct {
	bridge     *clientbridge.Bridge
	controller *client.Controller
	server     *libbox.CommandServer
	cancel     context.CancelFunc
	closeOnce  sync.Once
	platform   *nativePlatform
	updateMu   sync.Mutex
	updatePath string
	updateDone chan struct{}
	networks   networkChanges
}

func NewRuntime(dir string, callbacks Callbacks) (*Runtime, error) {
	if callbacks == nil {
		return nil, errors.New("Android platform is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := libbox.Setup(&libbox.SetupOptions{BasePath: dir, WorkingPath: dir, TempPath: filepath.Join(dir, "temp"), FixAndroidStack: true, OomKillerDisabled: true, LogMaxLines: 20}); err != nil {
		return nil, err
	}
	client.SetAndroidSocketProtector(func(fd int) error { return callbacks.Protect(int32(fd)) })
	client.SetAndroidResolver(callbacks.Lookup)
	platform := &nativePlatform{callbacks: callbacks}
	server, err := libbox.NewCommandServer(platform, platform)
	if err != nil {
		return nil, err
	}
	host := &embeddedHost{server: server, callbacks: callbacks}
	storage := &nativeStorage{callbacks: callbacks}
	controller, err := client.New(dir, storage, client.NewEmbeddedCore(filepath.Join(dir, "runtime"), storage, host))
	if err != nil {
		server.Close()
		return nil, err
	}
	if err = controller.SetMode(controller.View().Mode, true); err != nil {
		controller.Close()
		server.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &Runtime{controller: controller, server: server, cancel: cancel, platform: platform}
	runtime.bridge = &clientbridge.Bridge{Controller: controller, OpenURL: callbacks.OpenURL, Mobile: true}
	updater := client.NewBundleUpdater(filepath.Join(dir, "updates"), "android-"+goruntime.GOARCH, client.AppVersion, controller.BundleUpdateSource, func(path string, _ clientrelease.Release, _ clientrelease.Envelope) error {
		runtime.updateMu.Lock()
		runtime.updatePath = path
		runtime.updateMu.Unlock()
		return nil
	})
	runtime.bridge.Update = func(method string) (any, error) {
		if method == "updates" || method == "downloadUpdate" {
			_ = updater.Check(ctx)
		}
		if method == "installUpdate" {
			runtime.updateMu.Lock()
			path := runtime.updatePath
			runtime.updateMu.Unlock()
			if path == "" {
				return nil, errors.New("尚未下载可安装的更新")
			}
			if err := callbacks.InstallAPK(path); err != nil {
				return nil, errors.New("请允许 HQIP 安装更新后重试")
			}
		}
		return updater.View(), nil
	}
	runtime.updateDone = make(chan struct{})
	go func() { defer close(runtime.updateDone); updater.Run(ctx) }()
	go controller.Run(ctx)
	return runtime, nil
}

func (r *Runtime) Call(request string) string { return r.bridge.Call(request) }
func (r *Runtime) Disconnect()                { _ = r.controller.Disconnect() }

// Called by Android physical-network callbacks, never by the UI polling loop.
func (r *Runtime) NetworkChanged() {
	raw, err := r.platform.callbacks.Interfaces()
	if err != nil {
		return
	}
	_ = r.networks.update(raw, func() {
		r.platform.updateMonitor()
		r.server.ResetNetwork()
	})
}
func (r *Runtime) Close() {
	r.closeOnce.Do(func() { r.cancel(); <-r.updateDone; r.controller.Close(); r.server.Close() })
}

type embeddedHost struct {
	server    *libbox.CommandServer
	callbacks Callbacks
}

func (h *embeddedHost) Start(config string) error {
	// libbox's convenience wrapper writes configuration.json with node secrets.
	// StartedService retains the platform context created by NewCommandServer;
	// its public entry point skips only that diagnostic snapshot and export.
	return h.server.StartedService.StartOrReloadService(context.Background(), config, &daemon.OverrideOptions{})
}
func (h *embeddedHost) Stop() error {
	err := h.server.CloseService()
	closed := h.callbacks.CloseTun()
	if err != nil {
		return err
	}
	return closed
}

type nativeStorage struct{ callbacks Callbacks }

func (*nativeStorage) SecureDir(dir string) error               { return os.MkdirAll(dir, 0700) }
func (p *nativeStorage) Protect(value []byte) ([]byte, error)   { return p.callbacks.Encrypt(value) }
func (p *nativeStorage) Unprotect(value []byte) ([]byte, error) { return p.callbacks.Decrypt(value) }
func (*nativeStorage) Prepare(*exec.Cmd)                        {}
func (*nativeStorage) Attach(*exec.Cmd) (io.Closer, error) {
	return nil, errors.New("subprocesses unavailable on Android")
}
func (*nativeStorage) EnableProxy(int) error { return errors.New("Android requires VpnService") }
func (*nativeStorage) RestoreProxy() error   { return nil }
func (*nativeStorage) StartTUN(context.Context, []byte) (func() error, error) {
	return nil, errors.New("use embedded VPN engine")
}

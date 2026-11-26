//go:build android

package mobile

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/flutterrelease"
)

// PreviewCallbacks keeps the old Godot JNI contract unchanged.
type PreviewCallbacks interface {
	Callbacks
	Metered() bool
}

func NewPreviewRuntime(dir string, callbacks PreviewCallbacks) (*Runtime, error) {
	return newRuntime(dir, callbacks, true)
}

func (r *Runtime) startPreviewUpdates(ctx context.Context, dir string, callbacks Callbacks) {
	metered := func() bool { c, ok := callbacks.(PreviewCallbacks); return !ok || c.Metered() }
	var staged flutterrelease.Release
	updater := client.NewFlutterUpdater(filepath.Join(dir, "updates"), "android-"+runtime.GOARCH, r.controller.BundleUpdateSource, metered, func(path string, release flutterrelease.Release, _ flutterrelease.Envelope) error {
		r.updateMu.Lock()
		defer r.updateMu.Unlock()
		r.updatePath = path
		staged = release
		return nil
	})
	r.bridge.Update = func(method string) (any, error) {
		switch method {
		case "updates", "downloadUpdate":
			go func() { _ = updater.Check(ctx, true) }()
		case "installUpdate":
			r.updateMu.Lock()
			path := r.updatePath
			release := staged
			r.updateMu.Unlock()
			if path == "" || !updater.View().Ready {
				return nil, errors.New("更新尚未下载完成")
			}
			file, err := flutterrelease.VerifyFile(path, release)
			if err != nil {
				return nil, errors.New("更新文件已变化，请重新下载")
			}
			file.Close()
			if err := callbacks.InstallAPK(path); err != nil {
				return nil, errors.New("请允许 HQIP Preview 安装更新后重试")
			}
		}
		return updater.View(), nil
	}
	r.updateDone = make(chan struct{})
	go func() { defer close(r.updateDone); updater.Run(ctx) }()
}

//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/clientbridge"
	"github.com/KeepS13/HQIP/internal/flutterrelease"
	"github.com/KeepS13/HQIP/internal/previewhost"
	"github.com/KeepS13/HQIP/internal/winclient"
)

var errRestart = errors.New("restart requested")

func main() {
	attach := flag.Bool("attach", false, "private UI pipe proxy")
	background := flag.Bool("background", false, "start in tray")
	dir := flag.String("state-dir", "", "isolated preview state directory")
	marker := flag.String("startup-marker", "", "launcher readiness path")
	flag.Parse()
	var err error
	if *attach {
		err = proxy()
	} else {
		err = run(*dir, *marker, *background)
	}
	if errors.Is(err, errRestart) {
		os.Exit(42)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func proxy() error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, err := previewhost.Dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { _, _ = io.Copy(conn, os.Stdin); conn.Close() }()
	_, err = io.Copy(os.Stdout, conn)
	return err
}
func run(dir, marker string, background bool) error {
	if err := notifyExisting(); err == nil {
		return nil
	}
	listener, err := previewhost.Listen()
	if err != nil {
		return err
	}
	defer listener.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	base := filepath.Dir(executable)
	root := filepath.Dir(filepath.Dir(base))
	if dir == "" {
		dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "HQIP", "FlutterPreview")
	}
	platform, err := winclient.New(dir, filepath.Join(base, "sing-box.exe"))
	if err != nil {
		return err
	}
	controller, err := client.New(dir, platform, &client.Core{Path: filepath.Join(base, "sing-box.exe"), Dir: filepath.Join(dir, "runtime"), Platform: platform})
	if err != nil {
		return err
	}
	defer controller.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go controller.Run(ctx)
	var uiMu sync.Mutex
	var ui *exec.Cmd
	var restart bool
	var once sync.Once
	stop := func(update bool) { once.Do(func() { restart = update; cancel(); listener.Close(); postTrayQuit() }) }
	open := func() {
		uiMu.Lock()
		defer uiMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if ui != nil {
			focusProcess(uint32(ui.Process.Pid))
			return
		}
		command := exec.Command(filepath.Join(base, "hqip-ui.exe"))
		command.Dir = base
		if command.Start() != nil {
			return
		}
		ui = command
		go func() {
			_ = command.Wait()
			uiMu.Lock()
			if ui == command {
				ui = nil
			}
			uiMu.Unlock()
		}()
	}
	ready := func() {
		if marker != "" {
			_ = os.WriteFile(marker, []byte("ready"), 0600)
		}
	}
	bridge := &clientbridge.Bridge{Controller: controller, OpenURL: winclient.OpenBrowser, AutoStart: func(enabled *bool) (bool, error) {
		return winclient.PreviewAutoStart(filepath.Join(root, "HQIP Preview.exe"), enabled)
	}}
	updater := client.NewFlutterUpdater(filepath.Join(dir, "updates"), "windows-amd64", controller.BundleUpdateSource, nil, func(path string, r flutterrelease.Release, e flutterrelease.Envelope) error {
		return flutterrelease.Stage(root, path, r, e)
	})
	bridge.Update = func(method string) (any, error) {
		switch method {
		case "updates", "downloadUpdate":
			go func() { _ = updater.Check(ctx, true) }()
		case "installUpdate":
			if !updater.View().Ready {
				return nil, fmt.Errorf("更新尚未准备好")
			}
			go func() { time.Sleep(250 * time.Millisecond); stop(true) }()
		}
		return updater.View(), nil
	}
	updateDone := make(chan struct{})
	go func() { defer close(updateDone); updater.Run(ctx) }()
	host := &previewhost.Host{Bridge: bridge, OpenWindow: open, UIReady: ready, Quit: func() { stop(false) }}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go host.Serve(ctx, conn)
		}
	}()
	if background {
		ready()
	} else {
		open()
	}
	err = runTray(ctx, filepath.Join(base, "hqip.ico"), open, func() { go func() { _ = controller.Disconnect() }() }, func() { stop(false) }, func() string { return controller.View().RunState })
	stop(false)
	uiMu.Lock()
	if ui != nil {
		_ = ui.Process.Kill()
	}
	uiMu.Unlock()
	controller.Close()
	<-updateDone
	if restart {
		return errRestart
	}
	return err
}
func notifyExisting() error {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	conn, err := previewhost.Dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	return json.NewEncoder(conn).Encode(map[string]any{"id": 1, "method": "openWindow"})
}

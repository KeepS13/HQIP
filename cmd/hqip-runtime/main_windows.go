//go:build windows

// hqip-runtime is the native companion of the Godot UI. stdin EOF always closes
// the controller, restoring only network settings still owned by this instance.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/clientbridge"
	"github.com/KeepS13/HQIP/internal/clientrelease"
	"github.com/KeepS13/HQIP/internal/winclient"
)

func main() {
	stateDir := flag.String("state-dir", "", "isolated state directory for development")
	flag.Parse()
	if err := run(*stateDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dir string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	base := filepath.Dir(executable)
	if dir == "" {
		dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "HQIP", "Godot")
	}
	platform, err := winclient.New(dir, filepath.Join(base, "sing-box.exe"))
	if err != nil {
		return err
	}
	core := &client.Core{Path: filepath.Join(base, "sing-box.exe"), Dir: filepath.Join(dir, "runtime"), Platform: platform}
	controller, err := client.New(dir, platform, core)
	if err != nil {
		return err
	}
	defer controller.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go controller.Run(ctx)
	bridge := &clientbridge.Bridge{Controller: controller, OpenURL: winclient.OpenBrowser}
	root := base
	if filepath.Base(filepath.Dir(base)) == "versions" {
		root = filepath.Dir(filepath.Dir(base))
	}
	bridge.AutoStart = func(enabled *bool) (bool, error) {
		return winclient.GodotAutoStart(filepath.Join(root, "HQIP.exe"), enabled)
	}
	updater := client.NewBundleUpdater(filepath.Join(dir, "updates"), "windows-amd64", client.AppVersion, controller.BundleUpdateSource, func(path string, release clientrelease.Release, envelope clientrelease.Envelope) error {
		return clientrelease.StageWindows(root, path, release, envelope)
	})
	bridge.Update = func(method string) (any, error) {
		if method == "updates" || method == "downloadUpdate" {
			_ = updater.Check(ctx)
		}
		return updater.View(), nil
	}
	updaterDone := make(chan struct{})
	go func() { defer close(updaterDone); updater.Run(ctx) }()
	defer func() { cancel(); <-updaterDone }()
	return clientbridge.ServePipes(os.Stdin, os.Stdout, bridge, func() { cancel(); controller.Close() })
}

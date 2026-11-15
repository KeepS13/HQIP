package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/winclient"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/hqip.png
var appIcon []byte

type Desktop struct {
	controller *client.Controller
	updates    *client.UpdateChecker
}
type DesktopView struct {
	client.View
	Updates        client.UpdateView `json:"updates"`
	AutoStart      AutoStartState    `json:"autoStart"`
	ConnectionIcon ConnectionIcon    `json:"connectionIcon"`
}

func (d *Desktop) State() DesktopView {
	view := d.controller.View()
	return DesktopView{View: view, Updates: d.updates.View(), AutoStart: readAutoStartState(), ConnectionIcon: connectionIcon(view)}
}
func (d *Desktop) CheckUpdates()       { _ = d.updates.Check(true) }
func (d *Desktop) SnoozeUpdate() error { return d.updates.Snooze() }
func (d *Desktop) DownloadUpdate() error {
	url, err := d.updates.DownloadURL()
	if err != nil {
		return err
	}
	if err = winclient.OpenBrowser(url); err != nil {
		return err
	}
	return d.updates.Snooze()
}
func (d *Desktop) LoginBrowser(server string) error {
	return d.controller.LoginBrowser(server, winclient.OpenBrowser)
}
func (d *Desktop) OpenAccount() error {
	return winclient.OpenBrowser(client.AccountURL(d.controller.View().ServerURL))
}
func (d *Desktop) SelectSubscription(id string) error { return d.controller.SelectSubscription(id) }
func (d *Desktop) SelectSubscriptionNode(subscriptionID, nodeID string) error {
	return d.controller.SelectSubscriptionNode(subscriptionID, nodeID)
}
func (d *Desktop) MeasureDelay() error                 { return d.controller.MeasureDelay() }
func (d *Desktop) TestNodeDelays() error               { return d.controller.TestNodeDelays() }
func (d *Desktop) CancelNodeDelays()                   { d.controller.CancelNodeDelays() }
func (d *Desktop) CancelLogin()                        { d.controller.CancelLogin() }
func (d *Desktop) Logout() error                       { return d.controller.Logout() }
func (d *Desktop) Connect() error                      { return d.controller.Connect() }
func (d *Desktop) Disconnect() error                   { return d.controller.Disconnect() }
func (d *Desktop) Refresh() error                      { return d.controller.Refresh() }
func (d *Desktop) Select(id string) error              { return d.controller.Select(id) }
func (d *Desktop) SetMode(mode string, tun bool) error { return d.controller.SetMode(mode, tun) }

func main() {
	exe, e := os.Executable()
	if e != nil {
		log.Fatal(e)
	}
	base := filepath.Dir(exe)
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "HQIP", "Client")
	platform, e := winclient.New(dir, filepath.Join(base, "sing-box.exe"))
	if e != nil {
		log.Fatal(e)
	}
	core := &client.Core{Path: filepath.Join(base, "sing-box.exe"), Dir: filepath.Join(dir, "runtime"), Platform: platform}
	c, e := client.New(dir, platform, core)
	if e != nil {
		log.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	updates := client.NewUpdateChecker(dir, client.AppVersion, func() string { return c.View().ServerURL })
	defer updates.Close()
	d := &Desktop{controller: c, updates: updates}
	files, _ := fs.Sub(assets, "frontend/dist")
	var window *application.WebviewWindow
	app := application.New(application.Options{Name: "HQIP", Description: "HQIP 私人网络", Icon: appIcon, Services: []application.Service{application.NewService(d)}, Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(files)}, OnShutdown: func() { cancel(); c.Close() }, SingleInstance: &application.SingleInstanceOptions{UniqueID: "online.sharevps.hqip.client", OnSecondInstanceLaunch: func(application.SecondInstanceData) {
		if window != nil {
			window.Show()
			window.Focus()
		}
	}}})
	// Wails loads Windows icon resource ID 3 for the window/taskbar and falls
	// back to appIcon during development builds without generated resources.
	window = app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "HQIP · 私人网络", Frameless: true, Width: 1020, Height: 760, MinWidth: 760, MinHeight: 640, URL: "/", BackgroundColour: application.NewRGB(12, 17, 26)})
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { window.Hide(); event.Cancel() })
	go updates.Run(func() { window.Show(); window.Focus() })
	setupTray(app, window, c)
	if e = app.Run(); e != nil {
		log.Print("HQIP 窗口退出: ", e)
	}
}

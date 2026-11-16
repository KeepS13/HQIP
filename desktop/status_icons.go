package main

import (
	"context"
	"embed"
	"log"
	"time"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed assets/hqip-*.ico
var statusIconAssets embed.FS

type ConnectionIcon struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

func connectionIcon(view client.View) ConnectionIcon {
	if !view.LoggedIn {
		return ConnectionIcon{"disconnected", "未连接"}
	}
	if view.SelectionPending && view.ConnectionIntent {
		return ConnectionIcon{"connecting", "正在切换连接"}
	}
	switch view.RunState {
	case "recovering":
		return ConnectionIcon{"recovering", "恢复中"}
	case "connected":
		return ConnectionIcon{"connected", "已连接"}
	case "connecting":
		return ConnectionIcon{"connecting", "连接中"}
	case "failed":
		return ConnectionIcon{"failed", "连接失败"}
	default:
		return ConnectionIcon{"disconnected", "未连接"}
	}
}

func setupStatusIcons(app *application.App, window *application.WebviewWindow, tray *application.SystemTray, controller trayController) {
	ctx, cancel := context.WithCancel(context.Background())
	icons := newWindowStatusIcons(window)
	app.OnShutdown(func() {
		cancel()
		icons.close()
	})
	last := ConnectionIcon{}
	refresh := func() {
		// All icon handles and the last-applied state belong to the UI thread.
		if ctx.Err() != nil {
			return
		}
		state := connectionIcon(controller.View())
		if state == last {
			return
		}
		data, err := statusIconAssets.ReadFile("assets/hqip-" + state.Name + ".ico")
		if err == nil {
			err = icons.set(data)
		}
		if err != nil {
			log.Printf("HQIP 更新状态图标失败: %v", err)
			return
		}
		tray.SetIcon(data)
		tray.SetTooltip("HQIP · " + state.Label)
		last = state
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			application.InvokeAsync(refresh)
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					application.InvokeAsync(refresh)
				}
			}
		}()
	})
}

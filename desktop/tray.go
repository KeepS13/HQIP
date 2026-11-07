package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type trayController interface {
	View() client.View
	Connect() error
	Disconnect() error
	SelectSubscription(string) error
	Select(string) error
	SetMode(string, bool) error
}

type trayAction struct{ kind, value string }
type trayEntry struct {
	label    string
	disabled bool
	checked  bool
	check    bool
	action   trayAction
	children []trayEntry
}

func setupTray(app *application.App, window *application.WebviewWindow, controller trayController) {
	tray := app.SystemTray.New()
	tray.SetIcon(appIcon)
	tray.SetLabel("HQIP")
	menu := app.NewMenu()
	var busy atomic.Bool
	show := func() { window.Show(); window.Focus() }
	run := func(action trayAction) {
		switch action.kind {
		case "open":
			show()
			return
		case "quit":
			app.Quit()
			return
		}
		// Disconnect must remain available while a previous operation is pending.
		if action.kind != "disconnect" {
			if !busy.CompareAndSwap(false, true) {
				return
			}
			defer busy.Store(false)
		}
		if err := performTrayAction(controller, action); err != nil {
			app.Dialog.Error().SetTitle("HQIP · 操作未完成").SetMessage(err.Error()).Show()
		}
	}
	refresh := func() {
		clearTrayMenu(menu)
		appendTrayEntries(menu, trayEntries(controller.View(), busy.Load()), run)
		tray.SetMenu(menu)
	}
	refresh()
	// On Windows this runs on the UI thread, before TrackPopupMenu starts.
	// Rebuild on demand so selections, balance and errors reflect the latest
	// controller state without replacing a native menu while it is being used.
	menuOpen := false
	tray.OnRightClick(func() {
		application.InvokeSync(func() {
			if menuOpen {
				return
			}
			menuOpen = true
			defer func() { menuOpen = false }()
			refresh()
			tray.OpenMenu()
		})
	})
	tray.OnClick(show)
}

// Clear nested item registrations as well as the root. Clear leaves callbacks
// intact, since Wails may already have dispatched a previous click to a worker.
func clearTrayMenu(menu *application.Menu) {
	for i := 0; ; i++ {
		item := menu.ItemAt(i)
		if item == nil {
			break
		}
		if child := item.GetSubmenu(); child != nil {
			clearTrayMenu(child)
		}
	}
	menu.Clear()
}

func appendTrayEntries(menu *application.Menu, entries []trayEntry, run func(trayAction)) {
	for _, entry := range entries {
		if entry.label == "" {
			menu.AddSeparator()
			continue
		}
		if entry.children != nil {
			appendTrayEntries(menu.AddSubmenu(entry.label), entry.children, run)
			continue
		}
		var item *application.MenuItem
		if entry.check {
			item = menu.AddCheckbox(entry.label, entry.checked)
		} else {
			item = menu.Add(entry.label)
		}
		item.SetEnabled(!entry.disabled)
		if entry.action.kind != "" {
			action := entry.action
			item.OnClick(func(*application.Context) { run(action) })
		}
	}
}

func trayEntries(view client.View, busy bool) []trayEntry {
	entries := []trayEntry{{label: "打开 HQIP", action: trayAction{kind: "open"}}}
	if !view.LoggedIn {
		entries = append(entries, trayEntry{label: "账户未登录", disabled: true})
	} else if view.Subscription == nil {
		entries = append(entries, trayEntry{label: "尚未选择订阅", disabled: true})
	} else {
		entries = append(entries,
			trayEntry{label: "当前订阅：" + trayLabel(view.Subscription.TemplateName), disabled: true},
			trayEntry{label: "可用流量：" + trayBalance(*view.Subscription), disabled: true})
	}
	if view.SelectionPending || busy {
		entries = append(entries, trayEntry{label: "正在处理，请稍候…", disabled: true})
	}
	if view.LastError != "" {
		entries = append(entries, trayEntry{label: "查看错误：" + trayLabel(view.LastError), action: trayAction{kind: "open"}})
	}
	stoppable := view.ConnectionIntent || view.RunState == "connected" || view.RunState == "connecting"
	available := view.LoggedIn && !view.SelectionPending && !busy
	entries = append(entries, trayEntry{},
		trayEntry{label: "连接", disabled: !available || stoppable || !trayUsable(view.Subscription), action: trayAction{kind: "connect"}},
		trayEntry{label: "断开", disabled: !stoppable, action: trayAction{kind: "disconnect"}})
	subscriptions := []trayEntry{}
	for _, sub := range view.Subscriptions {
		if !trayUsable(&sub) {
			continue
		}
		status := traySubscriptionStatus(sub.Status)
		id := sub.ID
		if len(id) > 6 {
			id = id[len(id)-6:]
		}
		subscriptions = append(subscriptions, trayEntry{label: trayLabel(sub.TemplateName) + " · " + trayLabel(id) + " · " + status,
			check: true, checked: sub.ID == view.DesiredSubscriptionID, disabled: !available, action: trayAction{"subscription", sub.ID}})
	}
	if len(subscriptions) == 0 {
		subscriptions = append(subscriptions, trayEntry{label: "暂无有效订阅", disabled: true})
	}
	nodes := []trayEntry{}
	for _, node := range view.Nodes {
		label := trayLabel(node.Name)
		if node.ID == view.DefaultNodeID {
			label += " · 默认"
		}
		if !node.Ready {
			label += " · 暂不可用"
		}
		nodes = append(nodes, trayEntry{label: label, check: true, checked: node.ID == view.DesiredNodeID,
			disabled: !available || !trayUsable(view.Subscription) || !node.Ready, action: trayAction{"node", node.ID}})
	}
	if len(nodes) == 0 {
		nodes = append(nodes, trayEntry{label: "请先选择有效订阅", disabled: true})
	}
	return append(entries, trayEntry{},
		trayEntry{label: "切换订阅", children: subscriptions},
		trayEntry{label: "切换节点", children: nodes},
		trayEntry{label: "路由模式", children: []trayEntry{
			{label: "规则", check: true, checked: view.Mode != "global", disabled: !available, action: trayAction{"mode", "rule"}},
			{label: "全局", check: true, checked: view.Mode == "global", disabled: !available, action: trayAction{"mode", "global"}},
		}},
		trayEntry{label: "TUN 虚拟网卡", check: true, checked: view.TUNEnabled, disabled: !available, action: trayAction{kind: "tun"}},
		trayEntry{}, trayEntry{label: "退出并恢复网络", action: trayAction{kind: "quit"}})
}

// Revalidate when invoked: a subscription can expire or be removed while its
// menu is open. Read the other mode setting now, not from the menu snapshot.
func performTrayAction(controller trayController, action trayAction) error {
	if action.kind == "disconnect" {
		return controller.Disconnect()
	}
	view := controller.View()
	if !view.LoggedIn {
		return errors.New("请先打开 HQIP 登录账户")
	}
	if view.SelectionPending {
		return errors.New("请等待订阅切换完成")
	}
	switch action.kind {
	case "connect":
		if !trayUsable(view.Subscription) {
			return errors.New("请先选择有效的订阅")
		}
		return controller.Connect()
	case "subscription":
		for _, sub := range view.Subscriptions {
			if sub.ID == action.value && trayUsable(&sub) {
				return controller.SelectSubscription(action.value)
			}
		}
		return errors.New("该订阅已不可用，请重新选择")
	case "node":
		if trayUsable(view.Subscription) {
			for _, node := range view.Nodes {
				if node.ID == action.value && node.Ready {
					return controller.Select(action.value)
				}
			}
		}
		return errors.New("该节点已不可用，请重新选择")
	case "mode":
		return controller.SetMode(action.value, view.TUNEnabled)
	case "tun":
		mode := view.Mode
		if mode != "global" {
			mode = "rule"
		}
		return controller.SetMode(mode, !view.TUNEnabled)
	}
	return errors.New("未知托盘操作")
}

func trayUsable(sub *client.Subscription) bool {
	return sub != nil && sub.Status == "active" && sub.ExpiresAt.After(time.Now())
}

func trayBalance(sub client.Subscription) string {
	if sub.QuotaBytes == nil {
		return "无限"
	}
	n := max(int64(0), *sub.QuotaBytes-sub.UsedBytes)
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.2f TB", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1f KB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func trayLabel(label string) string {
	// Windows treats ampersands as menu mnemonics and tabs as shortcuts.
	label = strings.ReplaceAll(strings.Join(strings.Fields(label), " "), "&", "&&")
	runes := []rune(label)
	if len(runes) > 60 {
		return string(runes[:60]) + "…"
	}
	return label
}

func traySubscriptionStatus(status string) string {
	labels := map[string]string{"active": "有效", "expired": "已到期", "exhausted": "流量用尽", "disabled": "已停用", "removed": "已移除", "revoked": "已移除", "pending": "待生效"}
	if label, ok := labels[status]; ok {
		return label
	}
	return trayLabel(status)
}

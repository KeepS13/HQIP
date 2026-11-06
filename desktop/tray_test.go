package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func trayFixture() client.View {
	quota := int64(50e6)
	sub := client.Subscription{ID: "subscription-one", TemplateName: "测试50mb", Status: "active", ExpiresAt: time.Now().Add(time.Hour), QuotaBytes: &quota, UsedBytes: 33200000, ReservedBytes: 16800000}
	removed := sub
	removed.ID, removed.Status = "removed", "removed"
	return client.View{LoggedIn: true, Subscription: &sub, Subscriptions: []client.Subscription{sub, removed},
		Nodes: []client.NodeView{{ID: "node-a", Name: "香港节点", Ready: true}, {ID: "node-b", Name: "备用节点", Ready: false}}, DefaultNodeID: "node-a",
		Status: client.Status{DesiredSubscriptionID: sub.ID, DesiredNodeID: "node-a", Mode: "rule"}}
}

func TestTrayBalanceMatchesSettledQuota(t *testing.T) {
	view := trayFixture()
	sub := *view.Subscription
	if got := trayBalance(sub); got != "16.8 MB" {
		t.Fatalf("reserved quota must remain in displayed balance: %s", got)
	}
	sub.ReservedBytes = 0
	if got := trayBalance(sub); got != "16.8 MB" {
		t.Fatalf("settlement changed balance: %s", got)
	}
	sub.UsedBytes = 60e6
	if got := trayBalance(sub); got != "0 B" {
		t.Fatalf("over-quota balance: %s", got)
	}
	sub.QuotaBytes = nil
	if got := trayBalance(sub); got != "无限" {
		t.Fatalf("unlimited balance: %s", got)
	}
}

func TestTrayMenuRendersCurrentStateAndRefreshes(t *testing.T) {
	view := trayFixture()
	menu := application.NewMenu()
	defer clearTrayMenu(menu)
	appendTrayEntries(menu, trayEntries(view, false), func(trayAction) {})
	for label, enabled := range map[string]bool{"可用流量：16.8 MB": false, "连接": true, "断开": false, "备用节点 · 暂不可用": false, "测试50mb · emoved · 已移除": false} {
		item := menu.FindByLabel(label)
		if item == nil || item.Enabled() != enabled {
			t.Fatalf("menu item %q missing or enabled incorrectly", label)
		}
	}
	for _, label := range []string{"香港节点 · 默认", "规则", "测试50mb · on-one · 有效"} {
		if item := menu.FindByLabel(label); item == nil || !item.Checked() {
			t.Fatalf("current selection missing: %s", label)
		}
	}
	view.Mode, view.TUNEnabled, view.ConnectionIntent = "global", true, true
	view.Subscription.UsedBytes = 40e6
	view.Nodes = []client.NodeView{{ID: "replacement", Name: "新节点", Ready: true}}
	view.DesiredNodeID = "replacement"
	clearTrayMenu(menu)
	appendTrayEntries(menu, trayEntries(view, false), func(trayAction) {})
	if menu.FindByLabel("可用流量：10.0 MB") == nil || menu.FindByLabel("香港节点 · 默认") != nil {
		t.Fatal("menu retained stale quota or nodes")
	}
	if !menu.FindByLabel("全局").Checked() || !menu.FindByLabel("TUN 虚拟网卡").Checked() || menu.FindByLabel("连接").Enabled() || !menu.FindByLabel("断开").Enabled() {
		t.Fatal("updated mode, TUN or connection state was not reflected")
	}
}

func TestTrayUnavailableAndPendingStates(t *testing.T) {
	for _, state := range []string{"logged-out", "pending", "busy", "expired"} {
		t.Run(state, func(t *testing.T) {
			view := trayFixture()
			view.ConnectionIntent = true
			switch state {
			case "logged-out":
				view.LoggedIn = false
			case "pending":
				view.SelectionPending = true
			case "expired":
				view.Subscription.ExpiresAt = time.Now().Add(-time.Minute)
				view.Subscriptions[0].ExpiresAt = view.Subscription.ExpiresAt
			}
			menu := application.NewMenu()
			defer clearTrayMenu(menu)
			appendTrayEntries(menu, trayEntries(view, state == "busy"), func(trayAction) {})
			if menu.FindByLabel("连接").Enabled() || menu.FindByLabel("香港节点 · 默认").Enabled() || menu.FindByLabel("测试50mb · on-one · 有效").Enabled() {
				t.Fatal("unavailable operation enabled")
			}
			if !menu.FindByLabel("断开").Enabled() {
				t.Fatal("disconnect must remain available")
			}
		})
	}
}

type trayFake struct {
	view  client.View
	calls []trayAction
	tun   bool
	fail  error
}

func (f *trayFake) View() client.View { return f.view }
func (f *trayFake) Connect() error {
	f.calls = append(f.calls, trayAction{kind: "connect"})
	return f.fail
}
func (f *trayFake) Disconnect() error {
	f.calls = append(f.calls, trayAction{kind: "disconnect"})
	return f.fail
}
func (f *trayFake) SelectSubscription(id string) error {
	f.calls = append(f.calls, trayAction{"subscription", id})
	return f.fail
}
func (f *trayFake) Select(id string) error {
	f.calls = append(f.calls, trayAction{"node", id})
	return f.fail
}
func (f *trayFake) SetMode(mode string, tun bool) error {
	f.calls = append(f.calls, trayAction{"mode", mode})
	f.tun = tun
	return f.fail
}

func TestTrayActionsUseLatestSettingsAndController(t *testing.T) {
	f := &trayFake{view: trayFixture()}
	for _, action := range []trayAction{{"subscription", "subscription-one"}, {"node", "node-a"}, {kind: "connect"}, {kind: "disconnect"}} {
		if err := performTrayAction(f, action); err != nil || f.calls[len(f.calls)-1] != action {
			t.Fatalf("action %+v not forwarded: %v", action, err)
		}
	}
	f.view.TUNEnabled = true
	if err := performTrayAction(f, trayAction{"mode", "global"}); err != nil || !f.tun {
		t.Fatalf("mode change lost latest TUN state: %v", err)
	}
	f.view.Mode = "global"
	if err := performTrayAction(f, trayAction{kind: "tun"}); err != nil || f.tun || f.calls[len(f.calls)-1].value != "global" {
		t.Fatalf("TUN toggle lost latest mode: %v", err)
	}
	f.fail = errors.New("模拟操作失败")
	if err := performTrayAction(f, trayAction{kind: "connect"}); !errors.Is(err, f.fail) {
		t.Fatal("controller error was hidden")
	}
}

func TestTrayActionsRejectStaleMenuSelection(t *testing.T) {
	f := &trayFake{view: trayFixture()}
	for _, action := range []trayAction{{"subscription", "removed"}, {"subscription", "gone"}, {"node", "node-b"}, {"node", "gone"}} {
		if err := performTrayAction(f, action); err == nil {
			t.Fatalf("stale option accepted: %+v", action)
		}
	}
	f.view.SelectionPending = true
	if err := performTrayAction(f, trayAction{"mode", "global"}); err == nil {
		t.Fatal("setting changed during pending subscription switch")
	}
	f.view.LoggedIn = false
	if err := performTrayAction(f, trayAction{kind: "tun"}); err == nil {
		t.Fatal("logged-out action accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("invalid actions reached controller: %+v", f.calls)
	}
	if err := performTrayAction(f, trayAction{kind: "disconnect"}); err != nil || len(f.calls) != 1 {
		t.Fatal("disconnect was blocked by stale state")
	}
}

func TestTrayLabelsDoNotCreateNativeMenuShortcuts(t *testing.T) {
	if got := trayLabel("A&B\tName\nNext"); got != "A&&B Name Next" {
		t.Fatalf("unsafe menu label: %q", got)
	}
	if got := trayLabel(strings.Repeat("长", 100)); len([]rune(got)) != 61 {
		t.Fatal("long label was not bounded")
	}
}

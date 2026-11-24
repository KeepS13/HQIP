// Package clientbridge exposes a small, secret-free command surface to Godot.
// The host owns transport authentication/lifetime; the controller owns business
// state, cancellation and lock ordering. Commands never accept executable paths.
package clientbridge

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/KeepS13/HQIP/internal/client"
)

type Controller interface {
	View() client.View
	LoginBrowser(string, func(string) error) error
	CancelLogin()
	Logout() error
	Connect() error
	Disconnect() error
	Refresh() error
	SelectSubscription(string) error
	SelectSubscriptionNode(string, string) error
	SetMode(string, bool) error
	TestNodeDelays() error
	TestSelectedNodeDelay(string, string) error
	CancelSelectedNodeDelay(string, string)
	CancelNodeDelays()
	MeasureDelay() error
}

// Bridge is shared by Windows pipes and Android JNI. Call may run concurrently:
// cancellation must not queue behind a five-minute browser login operation.
type Bridge struct {
	Controller Controller
	OpenURL    func(string) error
	Mobile     bool
	Update     func(string) (any, error)
	AutoStart  func(*bool) (bool, error)

	// Capture and stamp under one lock; a later-delivered command reply must
	// not roll back a newer poll. Never hold this lock during a command/network
	// operation, so Disconnect still bypasses long login/selection operations.
	snapshotMu       sync.Mutex
	snapshotSequence uint64
}

type Request struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params struct {
		Server         string `json:"server"`
		SubscriptionID string `json:"subscriptionId"`
		NodeID         string `json:"nodeId"`
		Mode           string `json:"mode"`
		TUN            bool   `json:"tun"`
		Enabled        bool   `json:"enabled"`
	} `json:"params"`
}
type Response struct {
	ID     int64  `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}
type Snapshot struct {
	client.View
	SnapshotSequence uint64 `json:"snapshotSequence"`
}

type State struct {
	Snapshot
	Version   string        `json:"version"`
	Platform  string        `json:"platform"`
	Updates   any           `json:"updates,omitempty"`
	AutoStart AutoStartView `json:"autoStart"`
}

// snapshot owns ordering only within this Bridge's lifetime. It captures the
// controller's immutable View before incrementing; stamping after an unlocked
// View call could falsely mark an old capture as newer than a disconnect.
func (b *Bridge) snapshot() Snapshot {
	b.snapshotMu.Lock()
	defer b.snapshotMu.Unlock()
	view := b.Controller.View()
	b.snapshotSequence++
	return Snapshot{View: view, SnapshotSequence: b.snapshotSequence}
}

type AutoStartView struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Error     string `json:"error"`
}

func (b *Bridge) Call(raw string) string {
	var request Request
	response := Response{}
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &request) != nil || request.ID <= 0 {
		response.Error = "客户端命令无效"
	} else {
		response.ID = request.ID
		var err error
		response.Result, err = b.dispatch(request)
		if err != nil {
			response.Error = err.Error()
			response.Result = nil
		}
	}
	data, _ := json.Marshal(response)
	return string(data)
}

func (b *Bridge) dispatch(r Request) (any, error) {
	if b.Controller == nil {
		return nil, errors.New("客户端运行时尚未就绪")
	}
	p := r.Params
	switch r.Method {
	case "state":
		platform := "windows"
		if b.Mobile {
			platform = "android"
		}
		var updates any
		if b.Update != nil {
			updates, _ = b.Update("view")
		}
		startup := AutoStartView{Supported: b.AutoStart != nil}
		if b.AutoStart != nil {
			var err error
			startup.Enabled, err = b.AutoStart(nil)
			if err != nil {
				startup.Error = "读取开机自启设置失败，请重新设置"
			}
		}
		return State{Snapshot: b.snapshot(), Version: client.AppVersion, Platform: platform, Updates: updates, AutoStart: startup}, nil
	case "login":
		if p.Server == "" {
			p.Server = client.DefaultServerURL
		}
		return true, b.Controller.LoginBrowser(p.Server, b.OpenURL)
	case "cancelLogin":
		b.Controller.CancelLogin()
	case "logout":
		return true, b.Controller.Logout()
	case "connect":
		return true, b.Controller.Connect()
	case "disconnect":
		return true, b.Controller.Disconnect()
	case "refresh":
		return true, b.Controller.Refresh()
	case "subscription":
		if err := b.Controller.SelectSubscription(p.SubscriptionID); err != nil {
			return nil, err
		}
		return b.snapshot(), nil
	case "node":
		if err := b.Controller.SelectSubscriptionNode(p.SubscriptionID, p.NodeID); err != nil {
			return nil, err
		}
		return b.snapshot(), nil
	case "mode":
		return true, b.Controller.SetMode(p.Mode, p.TUN || b.Mobile)
	case "testNodes":
		return true, b.Controller.TestNodeDelays()
	case "testNode":
		return true, b.Controller.TestSelectedNodeDelay(p.SubscriptionID, p.NodeID)
	case "cancelNodeTest":
		b.Controller.CancelSelectedNodeDelay(p.SubscriptionID, p.NodeID)
	case "cancelTest":
		b.Controller.CancelNodeDelays()
	case "delay":
		return true, b.Controller.MeasureDelay()
	case "autoStart":
		if b.AutoStart == nil {
			return nil, errors.New("此平台不支持开机自启")
		}
		return b.AutoStart(&p.Enabled)
	case "account":
		return true, b.OpenURL(client.AccountURL(b.Controller.View().ServerURL))
	case "shop":
		return true, b.OpenURL(client.ShopURL(b.Controller.View().ServerURL))
	case "updates", "downloadUpdate", "installUpdate":
		if b.Update == nil {
			return nil, errors.New("此构建尚未配置更新渠道")
		}
		return b.Update(r.Method)
	default:
		return nil, errors.New("不支持的客户端命令")
	}
	return true, nil
}

package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/clientrelease"
	"github.com/KeepS13/HQIP/internal/flutterrelease"
)

var PreviewUpdatePublicKey string
var PreviewSequence = "1"

type FlutterUpdateView struct {
	Phase      string `json:"phase"`
	Version    string `json:"version"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Ready      bool   `json:"ready"`
	Message    string `json:"message"`
}
type FlutterUpdater struct {
	mu                 sync.Mutex
	busy               bool
	dir, platform, key string
	installed          int64
	source             func() string
	metered            func() bool
	stage              func(string, flutterrelease.Release, flutterrelease.Envelope) error
	http               *http.Client
	view               FlutterUpdateView
}

func NewFlutterUpdater(dir, platform string, source func() string, metered func() bool, stage func(string, flutterrelease.Release, flutterrelease.Envelope) error) *FlutterUpdater {
	sequence, _ := strconv.ParseInt(PreviewSequence, 10, 64)
	u := &FlutterUpdater{dir: dir, platform: platform, key: PreviewUpdatePublicKey, installed: sequence, source: source, metered: metered, stage: stage, view: FlutterUpdateView{Phase: "idle", Message: "预览版更新独立于正式客户端"}}
	u.http = &http.Client{Transport: previewProgressTransport{base: newDirectHTTPTransport(), updater: u}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return u
}
func (u *FlutterUpdater) View() FlutterUpdateView { u.mu.Lock(); defer u.mu.Unlock(); return u.view }
func (u *FlutterUpdater) set(phase, message string) {
	u.mu.Lock()
	u.view.Phase = phase
	u.view.Message = message
	u.mu.Unlock()
}
func (u *FlutterUpdater) Run(ctx context.Context) {
	defer u.http.CloseIdleConnections()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var last time.Time
	previous := ""
	for {
		base := u.source()
		if base != "" && (base != previous || time.Since(last) >= 6*time.Hour || (u.View().Phase == "available" && (u.metered == nil || !u.metered()))) {
			_ = u.Check(ctx, false)
			previous = base
			last = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (u *FlutterUpdater) Check(ctx context.Context, manual bool) error {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return nil
	}
	u.busy = true
	u.view.Ready = false
	u.view.Downloaded = 0
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.busy = false; u.mu.Unlock() }()
	u.set("checking", "正在检查预览版更新")
	err := u.check(ctx, manual)
	if err != nil {
		u.set("error", "更新暂不可用，可稍后重试；当前连接不受影响")
	}
	return err
}
func (u *FlutterUpdater) check(ctx context.Context, manual bool) error {
	base := u.source()
	if !validUpdateBase(base) {
		return errors.New("invalid update source")
	}
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(check, "GET", base+"/api/v3/client/releases?client=flutter&channel=preview", nil)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		u.set("idle", "预览更新渠道尚未开放，当前版本可继续使用")
		return nil
	}
	if resp.StatusCode != 200 {
		return errors.New("update catalog unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	var catalog flutterrelease.Catalog
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &catalog) != nil || len(catalog.Releases) > 16 {
		return errors.New("invalid update catalog")
	}
	seen, err := u.history()
	if err != nil {
		return err
	}
	var selected flutterrelease.Release
	var signed flutterrelease.Envelope
	for _, entry := range catalog.Releases {
		r, err := entry.Verify(u.key)
		if err != nil {
			return err
		}
		if r.Kind != "update" || r.Platform != u.platform || r.Sequence <= u.installed || r.Sequence < seen.Sequence {
			continue
		}
		if r.Sequence == seen.Sequence && r.SHA256 != seen.SHA256 || r.Sequence == selected.Sequence && r.SHA256 != selected.SHA256 {
			return errors.New("conflicting preview release")
		}
		if r.Sequence > selected.Sequence {
			selected = r
			signed = entry
		}
	}
	if selected.Sequence == 0 {
		u.set("idle", "当前已是此渠道的可用版本")
		return nil
	}
	if u.source() != base {
		return context.Canceled
	}
	if err = os.MkdirAll(u.dir, 0700); err != nil {
		return err
	}
	raw, _ = json.Marshal(signed)
	if err = clientrelease.WriteAtomic(filepath.Join(u.dir, "preview-seen.json"), raw); err != nil {
		return err
	}
	u.mu.Lock()
	u.view.Version = selected.Version
	u.view.Total = selected.Size
	u.mu.Unlock()
	if !manual && u.metered != nil && u.metered() {
		u.set("available", "发现新版本，连接 Wi-Fi 后重试或手动下载")
		return nil
	}
	path := filepath.Join(u.dir, selected.SHA256+"-"+selected.Filename)
	if verified, err := flutterrelease.VerifyFile(path, selected); err == nil {
		verified.Close()
	} else {
		u.set("downloading", "正在下载更新，当前连接继续保持")
		if err = u.download(ctx, base+selected.DownloadPath(), path, selected); err != nil {
			return err
		}
	}
	if u.source() != base {
		return context.Canceled
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	u.set("verifying", "正在校验并准备新版本")
	if err = u.stage(path, selected, signed); err != nil {
		return err
	}
	u.mu.Lock()
	u.view = FlutterUpdateView{Phase: "ready", Version: selected.Version, Downloaded: selected.Size, Total: selected.Size, Ready: true, Message: "更新已准备好，可选择稍后或立即安装"}
	u.mu.Unlock()
	return nil
}
func (u *FlutterUpdater) download(ctx context.Context, url, path string, r flutterrelease.Release) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(part)
	err = (&BundleUpdater{http: u.http}).transferBundle(ctx, url, f, r.Size)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	verified, err := flutterrelease.VerifyFile(part, r)
	if err != nil {
		return err
	}
	verified.Close()
	return os.Rename(part, path)
}
func (u *FlutterUpdater) history() (flutterrelease.Release, error) {
	raw, err := os.ReadFile(filepath.Join(u.dir, "preview-seen.json"))
	if errors.Is(err, os.ErrNotExist) {
		return flutterrelease.Release{}, nil
	}
	if err != nil {
		return flutterrelease.Release{}, err
	}
	var e flutterrelease.Envelope
	if len(raw) > 16384 || json.Unmarshal(raw, &e) != nil {
		return flutterrelease.Release{}, errors.New("invalid preview history")
	}
	r, err := e.Verify(u.key)
	if err == nil && (r.Platform != u.platform || r.Kind != "update") {
		err = errors.New("invalid preview history platform")
	}
	return r, err
}

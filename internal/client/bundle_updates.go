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
)

// Build-time public pin. Private signing keys never ship with the application.
var UpdatePublicKey string
var AppSequence = "5000001"

type BundleUpdateView struct {
	Checking bool   `json:"checking"`
	Ready    bool   `json:"ready"`
	Version  string `json:"version"`
	Message  string `json:"message"`
}
type BundleUpdater struct {
	mu            sync.Mutex
	dir, platform string
	source        func() string
	stage         func(string, clientrelease.Release, clientrelease.Envelope) error
	http          *http.Client
	view          BundleUpdateView
	publicKey     string
	installed     int64
	prepared      string // owned by the single Check worker
}

// NewBundleUpdater retains the display-version argument for source compatibility.
// Only the signed sequence orders releases; product version names may be reset.
func NewBundleUpdater(dir, platform, _ string, source func() string, stage func(string, clientrelease.Release, clientrelease.Envelope) error) *BundleUpdater {
	sequence, _ := strconv.ParseInt(AppSequence, 10, 64)
	return &BundleUpdater{dir: dir, platform: platform, source: source, stage: stage, publicKey: UpdatePublicKey, installed: sequence, http: &http.Client{Transport: newDirectHTTPTransport(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, view: BundleUpdateView{Message: "节点与规则自动同步，当前版本可继续使用"}}
}
func (u *BundleUpdater) View() BundleUpdateView { u.mu.Lock(); defer u.mu.Unlock(); return u.view }

// Run checks on startup and every six hours. A failed download changes only
// updater status; it cannot sign out, stop VPN or discard the current version.
func (u *BundleUpdater) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer u.http.CloseIdleConnections()
	var last time.Time
	previous := ""
	for {
		base := u.source()
		if base != "" && (base != previous || time.Since(last) > 6*time.Hour) {
			_ = u.Check(ctx)
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
func (u *BundleUpdater) Check(ctx context.Context) error {
	u.mu.Lock()
	if u.view.Checking {
		u.mu.Unlock()
		return nil
	}
	u.view.Checking = true
	u.mu.Unlock()
	err := u.checkAndStage(ctx)
	u.mu.Lock()
	u.view.Checking = false
	if err != nil {
		u.view.Message = "更新暂不可用，继续使用当前版本"
	}
	u.mu.Unlock()
	return err
}
func (u *BundleUpdater) checkAndStage(ctx context.Context) error {
	base := u.source()
	if !validUpdateBase(base) {
		return errors.New("update source unavailable")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(checkCtx, "GET", base+"/api/v3/client/releases", nil)
	req.Header.Set("Cache-Control", "no-cache")
	response, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// A compatible pre-Godot server can serve accounts without an update channel.
	if response.StatusCode == 404 {
		u.setMessage("当前服务尚未发布新版，现有功能可继续使用")
		return nil
	}
	if response.StatusCode != 200 {
		return errors.New("update service unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return errors.New("invalid update catalog")
	}
	var catalog clientrelease.Catalog
	if json.Unmarshal(raw, &catalog) != nil || len(catalog.Releases) > 16 {
		return errors.New("invalid update catalog")
	}
	var selected clientrelease.Release
	var signed clientrelease.Envelope
	seen, err := u.readHighWater()
	if err != nil {
		return err
	}
	for _, entry := range catalog.Releases {
		r, err := entry.Verify(u.publicKey)
		if err != nil {
			return err
		}
		// Sequence is authenticated and monotonic across display-version resets.
		// Installed and persisted high-water values both prevent package replay.
		if r.Platform != u.platform || r.APIMin > 3 || r.APIMax < 3 || normalizedVersion(r.Version) == "" || r.Sequence <= u.installed || r.Sequence < seen.Sequence {
			continue
		}
		if r.Sequence == seen.Sequence && r.SHA256 != seen.SHA256 {
			return errors.New("conflicting release sequence")
		}
		if selected.Sequence == r.Sequence && selected.SHA256 != r.SHA256 {
			return errors.New("conflicting release sequence")
		}
		if r.Sequence > selected.Sequence {
			selected, signed = r, entry
		}
	}
	if selected.Version == "" {
		if u.prepared == "" {
			u.setMessage("当前已是此渠道的可用版本")
		}
		return nil
	}
	if u.source() != base {
		return context.Canceled
	}
	if err = os.MkdirAll(u.dir, 0700); err != nil {
		return err
	}
	// Persist authenticated metadata before downloading: a failed newer download
	// must not let a replayed older catalog lower the remembered sequence.
	raw, _ = json.Marshal(signed)
	if err = clientrelease.WriteAtomic(filepath.Join(u.dir, "seen-release.json"), raw); err != nil {
		return err
	}
	if u.prepared == selected.SHA256 {
		return nil
	}
	path := filepath.Join(u.dir, selected.SHA256+"-"+selected.Filename)
	if file, verifyErr := clientrelease.VerifyFile(path, selected); verifyErr == nil {
		file.Close()
	} else {
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
	if err = u.stage(path, selected, signed); err != nil {
		return err
	}
	u.prepared = selected.SHA256
	u.mu.Lock()
	u.view.Ready = true
	u.view.Version = selected.Version
	u.view.Message = "新版已准备好，将在退出后更新"
	if u.platform == "android-arm64" || u.platform == "android-amd64" {
		u.view.Message = "新版已下载，点击安装并在系统提示中确认"
	}
	u.mu.Unlock()
	return nil
}
func (u *BundleUpdater) setMessage(message string) {
	u.mu.Lock()
	u.view.Message = message
	u.mu.Unlock()
}
func (u *BundleUpdater) download(ctx context.Context, url, path string, release clientrelease.Release) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	partial := path + ".part"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(partial)
	err = u.transferBundle(ctx, url, file, release.Size)
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	verified, err := clientrelease.VerifyFile(partial, release)
	if err != nil {
		return err
	}
	verified.Close()
	return os.Rename(partial, path)
}

// The high-water record is signed, platform-bound, and survives app restarts.
// Corrupt records fail closed for updates only; account and VPN keep running.
func (u *BundleUpdater) readHighWater() (clientrelease.Release, error) {
	raw, err := os.ReadFile(filepath.Join(u.dir, "seen-release.json"))
	if errors.Is(err, os.ErrNotExist) {
		return clientrelease.Release{}, nil
	}
	if err != nil {
		return clientrelease.Release{}, err
	}
	var envelope clientrelease.Envelope
	if len(raw) > 16384 || json.Unmarshal(raw, &envelope) != nil {
		return clientrelease.Release{}, errors.New("invalid release history")
	}
	r, err := envelope.Verify(u.publicKey)
	if err == nil && r.Platform != u.platform {
		err = errors.New("invalid release history platform")
	}
	return r, err
}

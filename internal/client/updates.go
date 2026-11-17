package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Official bundles inject the same version as their release manifest.
var AppVersion = "4.7.0-dev"

type UpdateView struct {
	CurrentVersion string     `json:"currentVersion"`
	LatestVersion  string     `json:"latestVersion"`
	Available      bool       `json:"available"`
	Prompt         bool       `json:"prompt"`
	Checking       bool       `json:"checking"`
	CheckedAt      *time.Time `json:"checkedAt"`
	Error          string     `json:"error"`
}
type updateRelease struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Dirty    bool   `json:"dirty"`
}
type updateReminder struct {
	Source    string    `json:"source"`
	Installed string    `json:"installed"`
	Version   string    `json:"version"`
	Until     time.Time `json:"until"`
}
type UpdateChecker struct {
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
	source        func() string
	base          string
	http          *http.Client
	view          UpdateView
	release       updateRelease
	reminder      updateReminder
	reminderPath  string
	wake          chan struct{}
	requestCancel context.CancelFunc
	closed        bool
	generation    uint64
}

func NewUpdateChecker(dir, version string, source func() string) *UpdateChecker {
	ctx, cancel := context.WithCancel(context.Background())
	transport := newDirectHTTPTransport()
	u := &UpdateChecker{ctx: ctx, cancel: cancel, source: source, wake: make(chan struct{}, 1), reminderPath: filepath.Join(dir, "update-reminder.json"), view: UpdateView{CurrentVersion: version}, http: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if data, err := os.ReadFile(u.reminderPath); err == nil && len(data) <= 4096 {
		_ = json.Unmarshal(data, &u.reminder)
	}
	return u
}

// Run checks immediately, then hourly. The minute tick also notices source
// changes without requiring a visible window. Close cancels and joins requests.
func (u *UpdateChecker) Run(notify func()) {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.workers.Add(1)
	u.mu.Unlock()
	defer u.workers.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	shown := ""
	for {
		v := u.View()
		if v.CheckedAt == nil || time.Since(*v.CheckedAt) >= time.Hour {
			_ = u.Check(false)
		}
		v = u.View()
		if !v.Prompt {
			shown = ""
		} else if versionIdentity(v.LatestVersion) != shown {
			shown = versionIdentity(v.LatestVersion)
			if notify != nil {
				notify()
			}
		}
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
		case <-u.wake:
		}
	}
}

func (u *UpdateChecker) sourceLocked() {
	base := strings.TrimRight(u.source(), "/")
	if base == u.base {
		return
	}
	if u.requestCancel != nil {
		u.requestCancel()
	}
	u.base = base
	u.generation++
	u.release = updateRelease{}
	u.view = UpdateView{CurrentVersion: u.view.CurrentVersion}
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

func (u *UpdateChecker) View() UpdateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sourceLocked()
	v := u.view
	if v.Available {
		v.Prompt = !u.snoozedLocked()
	}
	if v.CheckedAt != nil {
		at := *v.CheckedAt
		v.CheckedAt = &at
	}
	return v
}

func (u *UpdateChecker) snoozedLocked() bool {
	r := u.reminder
	return r.Source == u.base && versionIdentity(r.Installed) == versionIdentity(u.view.CurrentVersion) && versionIdentity(r.Version) == versionIdentity(u.release.Version) && time.Now().Before(r.Until)
}

func (u *UpdateChecker) Check(manual bool) error {
	u.mu.Lock()
	u.sourceLocked()
	if u.closed {
		u.mu.Unlock()
		return context.Canceled
	}
	if u.view.Checking {
		u.mu.Unlock()
		return nil
	}
	base, version := u.base, u.view.CurrentVersion
	generation := u.generation
	u.view.Checking = true
	u.view.Error = ""
	ctx, cancel := context.WithTimeout(u.ctx, 10*time.Second)
	u.requestCancel = cancel
	u.workers.Add(1)
	u.mu.Unlock()
	defer u.workers.Done()
	defer cancel()
	release, err := u.fetch(ctx, base, version)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sourceLocked()
	if u.closed || u.generation != generation {
		return context.Canceled
	}
	u.requestCancel = nil
	u.view.Checking = false
	now := time.Now()
	u.view.CheckedAt = &now
	if err != nil {
		u.release = updateRelease{}
		u.view.LatestVersion = ""
		u.view.Available = false
		u.view.Error = "暂时无法检查更新，请稍后重试"
		return err
	}
	u.release = release
	u.view.LatestVersion = release.Version
	u.view.Available = release.Version != ""
	if manual && u.view.Available {
		u.reminder = updateReminder{}
	}
	select {
	case u.wake <- struct{}{}:
	default:
	}
	return nil
}

func (u *UpdateChecker) Snooze() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sourceLocked()
	if !u.view.Available {
		return nil
	}
	u.reminder = updateReminder{Source: u.base, Installed: u.view.CurrentVersion, Version: u.release.Version, Until: time.Now().Add(24 * time.Hour)}
	data, _ := json.Marshal(u.reminder)
	tmp := u.reminderPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return errors.New("暂时无法保存提醒时间")
	}
	if err := os.Rename(tmp, u.reminderPath); err != nil {
		return errors.New("暂时无法保存提醒时间")
	}
	return nil
}

func (u *UpdateChecker) DownloadURL() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sourceLocked()
	if !u.view.Available || !validUpdateRelease(u.base, u.release) {
		return "", errors.New("暂无可下载的更新，请重新检查")
	}
	return u.release.URL, nil
}

func (u *UpdateChecker) Close() {
	u.mu.Lock()
	u.closed = true
	u.cancel()
	u.mu.Unlock()
	u.workers.Wait()
	u.http.CloseIdleConnections()
}

var fullVersion = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+].*)?$`)

func normalizedVersion(value string) string {
	value = strings.TrimPrefix(value, "v")
	if !fullVersion.MatchString(value) {
		return ""
	}
	parts := strings.SplitN(value, "+", 2)
	core := strings.SplitN(parts[0], "-", 2)
	if len(core) == 2 {
		for _, identifier := range strings.Split(core[1], ".") {
			if identifier == "" {
				return ""
			}
			for _, r := range identifier {
				if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '-') {
					return ""
				}
			}
			if numericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return ""
			}
		}
	}
	if len(parts) == 2 {
		for _, identifier := range strings.Split(parts[1], ".") {
			if identifier == "" {
				return ""
			}
			for _, r := range identifier {
				if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '-') {
					return ""
				}
			}
		}
	}
	return "v" + value
}
func versionIdentity(value string) string { return strings.SplitN(normalizedVersion(value), "+", 2)[0] }
func hasPrerelease(value string) bool     { return strings.Contains(versionIdentity(value), "-") }
func numericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func compareNumeric(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func compareVersion(a, b string) int {
	a = versionIdentity(a)
	b = versionIdentity(b)
	if a == "" || b == "" {
		return 0
	}
	partsA := strings.SplitN(strings.TrimPrefix(a, "v"), "-", 2)
	partsB := strings.SplitN(strings.TrimPrefix(b, "v"), "-", 2)
	coreA, coreB := strings.Split(partsA[0], "."), strings.Split(partsB[0], ".")
	for i := range coreA {
		if cmp := compareNumeric(coreA[i], coreB[i]); cmp != 0 {
			return cmp
		}
	}
	if len(partsA) == 1 && len(partsB) == 1 {
		return 0
	}
	if len(partsA) == 1 {
		return 1
	}
	if len(partsB) == 1 {
		return -1
	}
	preA, preB := strings.Split(partsA[1], "."), strings.Split(partsB[1], ".")
	for i := 0; i < len(preA) && i < len(preB); i++ {
		x, y := preA[i], preB[i]
		nx, ny := numericIdentifier(x), numericIdentifier(y)
		switch {
		case nx && ny:
			if cmp := compareNumeric(x, y); cmp != 0 {
				return cmp
			}
		case nx:
			return -1
		case ny:
			return 1
		default:
			if cmp := strings.Compare(x, y); cmp != 0 {
				return cmp
			}
		}
	}
	if len(preA) < len(preB) {
		return -1
	}
	if len(preA) > len(preB) {
		return 1
	}
	return 0
}
func validUpdateBase(base string) bool {
	u, err := url.Parse(base)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.Opaque == ""
}
func validUpdateRelease(base string, r updateRelease) bool {
	_, err := hex.DecodeString(r.SHA256)
	return validUpdateBase(base) && !r.Dirty && normalizedVersion(r.Version) != "" && r.Platform == "windows-amd64" && r.Filename == "HQIP-Windows-x64.zip" && r.Size > 0 && r.Size <= 500<<20 && len(r.SHA256) == 64 && err == nil && r.SHA256 == strings.ToLower(r.SHA256) && r.URL == base+"/downloads/"+r.SHA256+"/HQIP-Windows-x64.zip"
}
func (u *UpdateChecker) fetch(ctx context.Context, base, installed string) (updateRelease, error) {
	if !validUpdateBase(base) || normalizedVersion(installed) == "" {
		return updateRelease{}, errors.New("invalid update source or installed version")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/user/releases", nil)
	if err != nil {
		return updateRelease{}, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	response, err := u.http.Do(req)
	if err != nil {
		return updateRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return updateRelease{}, errors.New("release service unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return updateRelease{}, errors.New("invalid release response")
	}
	var body struct {
		Releases []updateRelease `json:"releases"`
	}
	if err = json.Unmarshal(data, &body); err != nil {
		return updateRelease{}, err
	}
	var latest updateRelease
	for _, r := range body.Releases {
		if r.Platform != "windows-amd64" {
			continue
		}
		if !validUpdateRelease(base, r) {
			return updateRelease{}, errors.New("untrusted release metadata")
		}
		// Stable clients never prompt to move onto a prerelease channel.
		v := normalizedVersion(r.Version)
		if hasPrerelease(v) || compareVersion(v, installed) <= 0 {
			continue
		}
		if latest.Version == "" || compareVersion(v, latest.Version) > 0 {
			latest = r
		}
	}
	return latest, nil
}

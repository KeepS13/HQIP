package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

type Controller struct {
	mu                  sync.Mutex
	syncMu              sync.Mutex
	applyMu             sync.Mutex
	routeMu             sync.Mutex
	reportMu            sync.Mutex
	dir                 string
	platform            Platform
	engine              Engine
	api                 *API
	saved               persisted
	bundle              rules.Bundle
	view                View
	etag                string
	generation          uint64
	selectionGeneration uint64
	cancelApply         context.CancelFunc
	closed              bool
	wake                chan struct{}
	authCancel          context.CancelFunc
	loginGeneration     uint64
}

func New(dir string, platform Platform, engine Engine) (*Controller, error) {
	if e := platform.SecureDir(dir); e != nil {
		return nil, e
	}
	c := &Controller{dir: dir, platform: platform, engine: engine, wake: make(chan struct{}, 1)}
	c.saved = persisted{InstallationID: randomID(), Mode: "rule", ServerURL: "https://hqip.5202345.xyz"}
	if encrypted, e := os.ReadFile(filepath.Join(dir, "session.dat")); e == nil {
		b, e := platform.Unprotect(encrypted)
		if e != nil {
			return nil, errors.New("本机凭据解密失败")
		}
		if e = json.Unmarshal(b, &c.saved); e != nil {
			return nil, e
		}
	}
	c.view = View{ServerURL: c.saved.ServerURL, Status: Status{RunState: "disconnected", Mode: c.saved.Mode, TUNEnabled: c.saved.TUN, CoreVersion: CoreVersion}}
	if c.saved.AccountVersion != 2 {
		c.saved.Tokens = Tokens{}
		c.saved.Profile = Profile{}
		c.saved.SelectedNodeID = ""
		c.saved.AccountVersion = 2
	}
	if c.saved.Tokens.RefreshToken != "" {
		a, e := NewAPI(c.saved.ServerURL, c.saved.Tokens)
		if e != nil {
			return nil, e
		}
		c.api = a
		c.view.LoggedIn = true
		c.view.DeviceName = c.saved.Profile.Name
		c.view.DesiredNodeID = c.saved.SelectedNodeID
		c.populateLocked(c.saved.Profile)
		if b, e := os.ReadFile(filepath.Join(dir, "rules.json")); e == nil {
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) == c.saved.Profile.Rules.Hash {
				_ = json.Unmarshal(b, &c.bundle)
			}
		}
	}
	return c, nil
}
func (c *Controller) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.view
	v.Nodes = append([]NodeView{}, v.Nodes...)
	return v
}
func (c *Controller) persistLocked() error {
	if c.api != nil {
		c.saved.Tokens = c.api.Tokens()
	}
	b, e := json.Marshal(c.saved)
	if e != nil {
		return e
	}
	encrypted, e := c.platform.Protect(b)
	if e != nil {
		return e
	}
	tmp := filepath.Join(c.dir, "session.tmp")
	if e = os.WriteFile(tmp, encrypted, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, filepath.Join(c.dir, "session.dat"))
}
func (c *Controller) populateLocked(p Profile) {
	c.view.DeviceName = p.Name
	c.view.Revision = p.Revision
	c.view.RulesVersion = p.Rules.Version
	c.view.DefaultNodeID = p.DefaultNodeID
	c.view.Entitlement = p.Entitlement
	c.view.Nodes = []NodeView{}
	for _, n := range p.Nodes {
		c.view.Nodes = append(c.view.Nodes, NodeView{n.ID, n.Name, n.Ready})
	}
}
func (c *Controller) Login(server, user, password string) error {
	return errors.New("请在系统浏览器中登录用户账户，旧设备账号不再可用")
}
func (c *Controller) Logout() error {
	c.CancelLogin()
	return c.logoutFor(nil)
}

func (c *Controller) logoutFor(expected *API) error {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	api := c.api
	if expected != nil && api != expected {
		c.mu.Unlock()
		return nil
	}
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.view.ConnectionIntent = false
	c.mu.Unlock()
	_ = c.engine.Stop()
	if api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = api.Logout(ctx)
		cancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = nil
	c.saved.Tokens = Tokens{}
	c.saved.Profile = Profile{}
	c.saved.SelectedNodeID = ""
	c.bundle = rules.Bundle{}
	c.etag = ""
	c.view = View{ServerURL: c.saved.ServerURL, Status: Status{Mode: c.saved.Mode, TUNEnabled: c.saved.TUN, RunState: "disconnected", CoreVersion: CoreVersion}}
	_ = os.Remove(filepath.Join(c.dir, "rules.json"))
	return c.persistLocked()
}
func (c *Controller) Run(ctx context.Context) {
	go c.accountWatchdog(ctx)
	timer := time.NewTimer(0)
	defer timer.Stop()
	delay := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-timer.C:
		}
		if c.View().LoggedIn {
			e := c.Sync(ctx)
			if e != nil {
				delay *= 2
				if delay > 60*time.Second {
					delay = 60 * time.Second
				}
			} else {
				delay = 5 * time.Second
			}
		}
		c.mu.Lock()
		aliveExpected := c.view.RunState == "connected"
		c.mu.Unlock()
		if aliveExpected && !c.engine.Alive() {
			c.fail("代理内核已停止，请手动重新连接")
			c.report()
		}
		c.checkAccountConnection(ctx)
		timer.Reset(delay)
	}
}
func (c *Controller) Refresh() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Sync(ctx)
}
func (c *Controller) Sync(ctx context.Context) error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.mu.Lock()
	api := c.api
	etag := c.etag
	c.mu.Unlock()
	if api == nil {
		return nil
	}
	p, newTag, unchanged, e := api.Profile(ctx, etag)
	if e != nil {
		c.mu.Lock()
		current := c.api == api
		if current {
			c.view.SyncError = e.Error()
		}
		c.mu.Unlock()
		if current && errors.Is(e, ErrUnauthorized) {
			_ = c.logoutFor(api)
		}
		return e
	}
	if unchanged {
		c.mu.Lock()
		if c.api != api {
			c.mu.Unlock()
			return nil
		}
		now := time.Now()
		c.view.LastSync = &now
		c.view.SyncError = ""
		_ = c.persistLocked()
		c.mu.Unlock()
		c.report()
		return nil
	}
	if !p.Enabled {
		return c.logoutFor(api)
	}
	if p.Entitlement == nil || p.Entitlement.Status != "active" || !p.Entitlement.ExpiresAt.After(time.Now()) {
		return c.cacheUnavailable(api, p)
	}
	c.mu.Lock()
	old := c.saved.Profile
	b := c.bundle
	selected := c.saved.SelectedNodeID
	c.mu.Unlock()
	if p.Revision < old.Revision {
		return nil
	}
	configurationChanged := connectionConfigurationChanged(old, p, selected)
	if old.Rules.Hash != p.Rules.Hash || len(b.Rules) == 0 {
		data, e := api.Rules(ctx, p.Rules.URL)
		if e != nil {
			return c.rejectProfile(api, p, e)
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != p.Rules.Hash {
			return c.rejectProfile(api, p, errors.New("规则校验失败，已停止代理；未覆盖本地规则缓存"))
		}
		if e = json.Unmarshal(data, &b); e != nil {
			return c.rejectProfile(api, p, errors.New("规则格式错误"))
		}
		if e = rules.Validate(b); e != nil {
			return c.rejectProfile(api, p, e)
		}
		// Serialize cache publication with logout/login so an older in-flight
		// profile response cannot restore rules after the active session changed.
		c.routeMu.Lock()
		c.mu.Lock()
		current := c.api == api
		c.mu.Unlock()
		if !current {
			c.routeMu.Unlock()
			return nil
		}
		tmp := filepath.Join(c.dir, "rules.tmp")
		if e = os.WriteFile(tmp, data, 0600); e == nil {
			e = os.Rename(tmp, filepath.Join(c.dir, "rules.json"))
		}
		c.routeMu.Unlock()
		if e != nil {
			return c.rejectProfile(api, p, e)
		}
	}
	c.mu.Lock()
	if c.api != api {
		c.mu.Unlock()
		return nil
	}
	if p.Revision < old.Revision {
		c.mu.Unlock()
		return nil
	}
	allowed := false
	for _, n := range p.Nodes {
		if n.ID == c.saved.SelectedNodeID {
			allowed = true
		}
	}
	if !allowed {
		c.saved.SelectedNodeID = p.DefaultNodeID
	}
	c.saved.Profile = p
	c.bundle = b
	c.etag = newTag
	c.populateLocked(p)
	c.view.DesiredNodeID = c.saved.SelectedNodeID
	now := time.Now()
	c.view.LastSync = &now
	c.view.SyncError = ""
	intent := c.view.ConnectionIntent
	// Usage/lease updates also change the profile ETag. They must not cancel a
	// slow connection attempt while its effective routing configuration is stable.
	apply := intent && configurationChanged
	if apply {
		c.generation++
		if c.cancelApply != nil {
			c.cancelApply()
		}
	}
	if !intent {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
		if c.view.RunState != "failed" {
			c.view.RunState = "cached"
		}
	} else if !apply && c.view.RunState == "connected" {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
	}
	e = c.persistLocked()
	c.mu.Unlock()
	if e != nil {
		return e
	}
	if apply {
		go c.applyLatest()
	} else {
		c.report()
	}
	return nil
}

// A rejected new profile must not leave the previous route running, even when
// that previous node remains an allowed member of the newly assigned set.
func (c *Controller) rejectProfile(api *API, p Profile, err error) error {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.api != api {
		c.mu.Unlock()
		return err
	}
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.saved.Profile = p
	c.saved.SelectedNodeID = p.DefaultNodeID
	c.bundle = rules.Bundle{}
	c.etag = ""
	c.populateLocked(p)
	c.view.DesiredNodeID = p.DefaultNodeID
	c.view.ActualNodeID = ""
	c.view.ProcessedConfigRevision = p.Revision
	c.view.SyncError = err.Error()
	c.view.LastError = err.Error()
	if c.view.ConnectionIntent {
		c.view.RunState = "failed"
		c.view.ConnectionIntent = false
	}
	_ = c.persistLocked()
	c.mu.Unlock()
	_ = c.engine.Stop()
	c.report()
	return err
}
func (c *Controller) syncFailure(e error) error {
	c.mu.Lock()
	c.view.SyncError = e.Error()
	c.mu.Unlock()
	return e
}
func (c *Controller) Connect() error {
	c.mu.Lock()
	if c.api == nil {
		c.mu.Unlock()
		return errors.New("请先登录用户账户")
	}
	if c.view.Entitlement == nil || c.view.Entitlement.Status != "active" || !c.view.Entitlement.ExpiresAt.After(time.Now()) {
		c.mu.Unlock()
		return errors.New("套餐未开通、已到期或流量已用尽")
	}
	if len(c.bundle.Rules) == 0 {
		c.mu.Unlock()
		return errors.New("订阅尚未完整同步，请先刷新")
	}
	c.view.ConnectionIntent = true
	c.view.LastError = ""
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	go c.applyLatest()
	return nil
}
func (c *Controller) Disconnect() error {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	c.view.ConnectionIntent = false
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	e := c.engine.Stop()
	c.mu.Lock()
	c.view.ActualNodeID = ""
	c.view.RunState = "disconnected"
	c.view.LastError = ""
	c.mu.Unlock()
	c.report()
	return e
}
func (c *Controller) Select(id string) error {
	c.mu.Lock()
	found := false
	for _, n := range c.saved.Profile.Nodes {
		if n.ID == id {
			found = true
		}
	}
	if !found {
		c.mu.Unlock()
		return errors.New("节点未获此设备授权")
	}
	c.saved.SelectedNodeID = id
	c.selectionGeneration++
	c.view.DesiredNodeID = id
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	on := c.view.ConnectionIntent
	e := c.persistLocked()
	c.mu.Unlock()
	if e == nil && on {
		go c.applyLatest()
	}
	return e
}
func (c *Controller) SetMode(mode string, tun bool) error {
	if mode != "rule" && mode != "global" {
		return errors.New("模式无效")
	}
	c.mu.Lock()
	c.saved.Mode = mode
	c.saved.TUN = tun
	c.view.Mode = mode
	c.view.TUNEnabled = tun
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	on := c.view.ConnectionIntent
	e := c.persistLocked()
	c.mu.Unlock()
	if e == nil && on {
		go c.applyLatest()
	}
	return e
}
func (c *Controller) applyLatest() {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	if c.closed || !c.view.ConnectionIntent || c.api == nil {
		c.mu.Unlock()
		return
	}
	g := c.generation
	p := c.saved.Profile
	b := c.bundle
	id := c.saved.SelectedNodeID
	mode := c.saved.Mode
	tun := c.saved.TUN
	server := c.saved.ServerURL
	api := c.api
	ctx, cancel := context.WithTimeout(context.Background(), 135*time.Second)
	c.cancelApply = cancel
	c.view.RunState = "connecting"
	c.view.LastError = ""
	c.mu.Unlock()
	// Release the old transport before any candidate consumes another slot.
	e := c.engine.Stop()
	if e == nil {
		e = api.Drain(ctx)
	}
	if e == nil {
		e = api.WaitDrained(ctx)
	}
	if e == nil {
		e = c.engine.Apply(ctx, p, b, id, mode, tun, server)
	}
	if e != nil && ctx.Err() == nil {
		if state, err := api.Connections(ctx); err == nil && state.Message != "" {
			e = errors.New(state.Message)
		}
	}
	cancel()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	if e != nil {
		_ = c.engine.Stop()
	}
	c.mu.Lock()
	if g != c.generation || !c.view.ConnectionIntent || c.closed {
		c.mu.Unlock()
		_ = c.engine.Stop()
		return
	}
	if e != nil {
		c.view.RunState = "failed"
		c.view.ConnectionIntent = false
		c.view.ActualNodeID = ""
		c.view.LastError = e.Error()
		c.view.ProcessedConfigRevision = p.Revision
	} else {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
		c.view.ActualNodeID = id
		c.view.RunState = "connected"
		c.view.LastError = ""
	}
	c.mu.Unlock()
	c.report()
}
func (c *Controller) fail(message string) {
	_ = c.engine.Stop()
	c.mu.Lock()
	c.view.RunState = "failed"
	c.view.ConnectionIntent = false
	c.view.ActualNodeID = ""
	c.view.LastError = message
	c.mu.Unlock()
}
func (c *Controller) report() {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.mu.Lock()
	api := c.api
	s := c.view.Status
	c.mu.Unlock()
	if api == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = api.Report(ctx, s)
	c.mu.Lock()
	if c.api == api {
		_ = c.persistLocked()
	}
	c.mu.Unlock()
}
func (c *Controller) Close() {
	c.CancelLogin()
	c.mu.Lock()
	c.closed = true
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.view.ConnectionIntent = false
	c.mu.Unlock()
	_ = c.engine.Stop()
}

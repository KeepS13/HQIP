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
	logMu               sync.Mutex
	recoveryProbeMu     sync.Mutex
	recovery            *recoveryRun
	recoveryReady       bool
	suspended           bool
	now                 func() time.Time
	workers             sync.WaitGroup
	closeOnce           sync.Once
	workCtx             context.Context
	cancelWork          context.CancelFunc
	dir                 string
	platform            Platform
	engine              Engine
	api                 *API
	saved               persisted
	bundle              rules.Bundle
	view                View
	etag                string
	generation          uint64
	appliedGeneration   uint64
	selectionGeneration uint64
	cancelApply         context.CancelFunc
	closed              bool
	wake                chan struct{}
	authCancel          context.CancelFunc
	loginGeneration     uint64
	cancelSelection     context.CancelFunc
	restoreSelection    bool
	nodeTest            *nodeTestRun
	nodeProbe           func(context.Context, Node) (int64, error)
	autoNodeTestAPI     *API
}

func New(dir string, platform Platform, engine Engine) (*Controller, error) {
	if e := platform.SecureDir(dir); e != nil {
		return nil, e
	}
	c := &Controller{dir: dir, platform: platform, engine: engine, wake: make(chan struct{}, 1)}
	c.saved = persisted{AccountVersion: 3, InstallationID: randomID(), Mode: "rule", ServerURL: DefaultServerURL, Preferences: map[string]SelectionPreference{}}
	if encrypted, e := os.ReadFile(filepath.Join(dir, "session.dat")); e == nil {
		b, e := platform.Unprotect(encrypted)
		if e != nil {
			return nil, errors.New("本机凭据解密失败")
		}
		if e = json.Unmarshal(b, &c.saved); e != nil {
			return nil, e
		}
	}
	previousServerURL := c.saved.ServerURL
	c.saved.ServerURL = migrateOfficialServerURL(c.saved.ServerURL)
	c.view = View{ServerURL: c.saved.ServerURL, Status: Status{RunState: "disconnected", Mode: c.saved.Mode, TUNEnabled: c.saved.TUN, CoreVersion: CoreVersion}}
	if c.saved.AccountVersion != 3 {
		c.saved.Tokens = Tokens{}
		c.saved.Profile = Profile{}
		c.saved.SelectedNodeID = ""
		c.saved.SelectedSubscriptionID = ""
		c.saved.Preferences = map[string]SelectionPreference{}
		c.saved.AccountVersion = 3
	}
	if c.saved.Preferences == nil {
		c.saved.Preferences = map[string]SelectionPreference{}
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
		c.view.DesiredSubscriptionID = c.saved.SelectedSubscriptionID
		c.restoreSelection = true
		c.populateLocked(c.saved.Profile)
		if b, e := os.ReadFile(filepath.Join(dir, "rules.json")); e == nil {
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) == c.saved.Profile.Rules.Hash {
				_ = json.Unmarshal(b, &c.bundle)
			}
		}
	}
	c.workCtx, c.cancelWork = context.WithCancel(context.Background())
	if c.saved.ServerURL != previousServerURL {
		if err := c.persistLocked(); err != nil {
			c.cancelWork()
			return nil, err
		}
	}
	return c, nil
}
func (c *Controller) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateNodeTestLocked()
	v := c.view
	v.Nodes = append([]NodeView{}, v.Nodes...)
	v.Subscriptions = append([]Subscription{}, v.Subscriptions...)
	v.Metrics.History = append([]MetricPoint{}, v.Metrics.History...)
	v.NodeTests.Results = append([]NodeDelayResult{}, v.NodeTests.Results...)
	v.NodeChoices, _ = nodeChoices(c.saved.Profile)
	return v
}
func (c *Controller) persistLocked() error {
	if c.closed {
		return context.Canceled
	}
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
	c.view.Subscription = p.Subscription
	c.view.Subscriptions = append([]Subscription{}, p.Subscriptions...)
	c.view.SelectionRevision = p.SelectionRevision
	c.view.Nodes = []NodeView{}
	for _, n := range p.Nodes {
		c.view.Nodes = append(c.view.Nodes, NodeView{n.ID, n.Name, n.Ready})
	}
}
func (c *Controller) Logout() error {
	c.CancelLogin()
	return c.logoutFor(nil)
}

func (c *Controller) logoutFor(expected *API) error {
	return c.endSession(expected, false)
}

// Expiry belongs to the account session, not one connection generation. A
// delayed response from a replaced API must never sign out the new account.
func (c *Controller) expireSession(expected *API) error {
	if expected == nil {
		return nil
	}
	return c.endSession(expected, true)
}

func (c *Controller) endSession(expected *API, expired bool) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	api := c.api
	if expected != nil && api != expected {
		c.mu.Unlock()
		return nil
	}
	c.generation++
	c.cancelRecoveryLocked()
	c.selectionGeneration++
	if c.cancelSelection != nil {
		c.cancelSelection()
	}
	if c.cancelApply != nil {
		c.cancelApply()
	}
	if c.nodeTest != nil {
		c.nodeTest.cancel()
		c.nodeTest = nil
	}
	c.view.ConnectionIntent = false
	c.mu.Unlock()
	stopErr := c.engine.Stop()
	// Rejected credentials cannot revoke anything remotely. Do not make login
	// expiry wait on another network request or attempt a second token refresh.
	if api != nil && !expired {
		ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Second)
		_ = api.Logout(ctx)
		cancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = nil
	c.saved.Tokens = Tokens{}
	c.saved.Profile = Profile{}
	c.saved.SelectedNodeID = ""
	c.saved.SelectedSubscriptionID = ""
	c.cancelSelection = nil
	c.cancelApply = nil
	c.autoNodeTestAPI = nil
	c.restoreSelection = false
	c.suspended = false
	c.bundle = rules.Bundle{}
	c.etag = ""
	c.view = View{ServerURL: c.saved.ServerURL, Status: Status{Mode: c.saved.Mode, TUNEnabled: c.saved.TUN, RunState: "disconnected", CoreVersion: CoreVersion}}
	if expired {
		c.view.LastError = ErrUnauthorized.Error()
	}
	_ = os.Remove(filepath.Join(c.dir, "rules.json"))
	persistErr := c.persistLocked()
	if persistErr != nil {
		// If writing the logged-out state fails, remove the old encrypted session
		// rather than restoring its invalid credentials on the next launch.
		if err := os.Remove(filepath.Join(c.dir, "session.dat")); err != nil && !os.IsNotExist(err) {
			persistErr = errors.Join(persistErr, err)
		}
		_ = os.Remove(filepath.Join(c.dir, "session.tmp"))
	}
	return errors.Join(stopErr, persistErr)
}
func (c *Controller) Run(ctx context.Context) {
	if !c.beginWork() {
		return
	}
	defer c.workers.Done()
	ctx, cancel := c.workContext(ctx)
	defer cancel()
	c.launch(func() { c.accountWatchdog(ctx) })
	c.launch(func() { c.runMetrics(ctx) })
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
				c.startAutomaticNodeTests()
			}
		}
		c.mu.Lock()
		aliveExpected := connectionRunning(c.view.RunState)
		generation := c.generation
		c.mu.Unlock()
		if aliveExpected && !c.engine.Alive() {
			c.failGeneration(generation, "代理内核已停止，请手动重新连接")
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
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	ctx, cancel := c.workContext(ctx)
	defer cancel()
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.mu.Lock()
	api := c.api
	etag := c.etag
	selection := c.selectionGeneration
	generation := c.generation
	pending := c.view.SelectionPending
	c.mu.Unlock()
	if api == nil || pending {
		return nil
	}
	p, newTag, unchanged, e := api.Profile(ctx, etag)
	if e != nil {
		if errors.Is(e, ErrUnauthorized) {
			_ = c.expireSession(api)
			return e
		}
		if errors.Is(e, context.Canceled) {
			return e
		}
		c.mu.Lock()
		current := c.api == api && c.selectionGeneration == selection && c.generation == generation
		if current {
			if retryable(e) && c.recoveryReady {
				c.startRecoveryLocked("account_unavailable")
			} else {
				c.view.SyncError = e.Error()
			}
		}
		c.mu.Unlock()
		if current && !retryable(e) && !errors.Is(e, context.Canceled) {
			c.failGeneration(generation, e.Error())
		}
		return e
	}
	if unchanged {
		c.mu.Lock()
		if c.api != api || selection != c.selectionGeneration || generation != c.generation {
			c.mu.Unlock()
			return nil
		}
		now := c.clockNow()
		c.view.LastSync = &now
		c.view.SyncError = ""
		_ = c.persistLocked()
		c.mu.Unlock()
		c.report()
		return nil
	}
	c.mu.Lock()
	currentSelection := c.api == api && c.selectionGeneration == selection && generation == c.generation && !c.view.SelectionPending
	c.mu.Unlock()
	if !currentSelection {
		return nil
	}
	if !p.Enabled {
		_ = c.expireSession(api)
		return ErrUnauthorized
	}
	if subscriptionFailure(p, c.clockNow()) != nil {
		err := c.cacheUnavailable(api, p, generation)
		c.restoreRemembered(api, p)
		return err
	}
	c.mu.Lock()
	old := c.saved.Profile
	b := c.bundle
	selected := c.saved.SelectedNodeID
	c.mu.Unlock()
	if p.SelectionRevision < old.SelectionRevision || (p.SubscriptionID == old.SubscriptionID && p.Revision < old.Revision) {
		return nil
	}
	configurationChanged := connectionConfigurationChanged(old, p, selected)
	if old.Rules.Hash != p.Rules.Hash || len(b.Rules) == 0 {
		data, e := api.Rules(ctx, p.Rules.URL)
		if e != nil {
			if errors.Is(e, ErrUnauthorized) {
				_ = c.expireSession(api)
				return e
			}
			if retryable(e) {
				c.mu.Lock()
				if c.api == api && c.generation == generation {
					c.startRecoveryLocked("rules_unavailable")
				}
				c.mu.Unlock()
				return e
			}
			return c.rejectProfile(api, p, generation, e)
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != p.Rules.Hash {
			return c.rejectProfile(api, p, generation, errors.New("规则校验失败，已停止代理；未覆盖本地规则缓存"))
		}
		if e = json.Unmarshal(data, &b); e != nil {
			return c.rejectProfile(api, p, generation, errors.New("规则格式错误"))
		}
		if e = rules.Validate(b); e != nil {
			return c.rejectProfile(api, p, generation, e)
		}
		// Serialize cache publication with logout/login so an older in-flight
		// profile response cannot restore rules after the active session changed.
		c.routeMu.Lock()
		c.mu.Lock()
		current := c.api == api && c.selectionGeneration == selection && generation == c.generation && !c.view.SelectionPending
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
			return c.rejectProfile(api, p, generation, e)
		}
	}
	c.mu.Lock()
	if c.api != api || c.selectionGeneration != selection || generation != c.generation || c.view.SelectionPending {
		c.mu.Unlock()
		return nil
	}
	if p.SelectionRevision < c.saved.Profile.SelectionRevision || (p.SubscriptionID == c.saved.Profile.SubscriptionID && p.Revision < c.saved.Profile.Revision) {
		c.mu.Unlock()
		return nil
	}
	if c.recoveryReady {
		authorized := p.SubscriptionID == c.saved.SelectedSubscriptionID
		if !authorized {
			c.mu.Unlock()
			c.failGeneration(generation, "当前订阅授权已移除，VPN 已关闭")
			return nil
		}
	}
	if p.SubscriptionID != c.saved.SelectedSubscriptionID {
		c.saved.SelectedNodeID = c.rememberedNodeLocked(p)
	}
	allowed := false
	for _, n := range p.Nodes {
		if n.ID == c.saved.SelectedNodeID {
			allowed = true
		}
	}
	if !allowed {
		c.saved.SelectedNodeID = automaticNode(p)
	}
	c.saved.Profile = p
	c.saved.SelectedSubscriptionID = p.SubscriptionID
	c.view.DesiredSubscriptionID = p.SubscriptionID
	c.rememberLocked()
	c.restoreSelection = false
	c.bundle = b
	c.etag = newTag
	c.populateLocked(p)
	c.view.DesiredNodeID = c.saved.SelectedNodeID
	if c.saved.SelectedNodeID == "" {
		c.view.LastError = "暂无可用节点，请稍后刷新或手动选择节点"
	} else if c.view.LastError == "暂无可用节点，请稍后刷新或手动选择节点" {
		c.view.LastError = ""
	}
	now := c.clockNow()
	c.view.LastSync = &now
	c.view.SyncError = ""
	intent := c.view.ConnectionIntent
	// Usage/lease updates also change the profile ETag. They must not cancel a
	// slow connection attempt while its effective routing configuration is stable.
	apply := intent && configurationChanged
	if apply {
		c.generation++
		c.cancelRecoveryLocked()
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
	} else if !apply && connectionRunning(c.view.RunState) {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
	}
	e = c.persistLocked()
	c.mu.Unlock()
	if e != nil {
		return e
	}
	if apply {
		c.launch(c.applyLatest)
	} else {
		c.report()
	}
	return nil
}

// A rejected new profile must not leave the previous route running, even when
// that previous node remains an allowed member of the newly assigned set.
func (c *Controller) rejectProfile(api *API, p Profile, generation uint64, err error) error {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.api != api || c.generation != generation || c.view.SelectionPending || p.SelectionRevision < c.saved.Profile.SelectionRevision {
		c.mu.Unlock()
		return err
	}
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.saved.Profile = p
	c.saved.SelectedNodeID = p.DefaultNodeID
	c.saved.SelectedSubscriptionID = p.SubscriptionID
	c.bundle = rules.Bundle{}
	c.etag = ""
	c.populateLocked(p)
	c.view.DesiredNodeID = p.DefaultNodeID
	c.view.DesiredSubscriptionID = p.SubscriptionID
	c.view.ActualNodeID = ""
	c.view.ActualSubscriptionID = ""
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
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.api == nil {
		c.mu.Unlock()
		return errors.New("请先登录用户账户")
	}
	if c.view.SelectionPending {
		c.mu.Unlock()
		return errors.New("正在切换订阅，请稍后连接")
	}
	if c.view.DesiredSubscriptionID != c.saved.SelectedSubscriptionID {
		c.mu.Unlock()
		return errors.New("订阅切换尚未完成，请重新选择订阅后再连接")
	}
	if !usableSubscription(c.saved.Profile) {
		c.mu.Unlock()
		return errors.New("请先选择一份有效订阅；已到期或耗尽的订阅不可连接")
	}
	if len(c.bundle.Rules) == 0 {
		c.mu.Unlock()
		return errors.New("订阅尚未完整同步，请先刷新")
	}
	c.view.ConnectionIntent = true
	c.view.LastError = ""
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	c.launch(c.applyLatest)
	return nil
}
func (c *Controller) Disconnect() error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.mu.Lock()
	if c.recovery != nil {
		c.recovery.cancel()
	}
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	c.view.ConnectionIntent = false
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	e := c.engine.Stop()
	c.mu.Lock()
	c.view.ActualNodeID = ""
	c.view.ActualSubscriptionID = ""
	c.view.RunState = "disconnected"
	c.view.LastError = ""
	c.mu.Unlock()
	c.report()
	return e
}
func (c *Controller) Select(id string) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.view.SelectionPending {
		c.mu.Unlock()
		return errors.New("请等待订阅切换完成后选择节点")
	}
	found := false
	for _, n := range c.saved.Profile.Nodes {
		if n.ID == id {
			found = true
		}
	}
	if !found {
		c.mu.Unlock()
		return errors.New("节点不属于本机当前订阅")
	}
	c.saved.SelectedNodeID = id
	c.rememberLocked()
	c.selectionGeneration++
	c.view.DesiredNodeID = id
	if c.view.LastError == "暂无可用节点，请稍后刷新或手动选择节点" {
		c.view.LastError = ""
	}
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	on := c.view.ConnectionIntent
	e := c.persistLocked()
	c.mu.Unlock()
	if e == nil && on {
		c.launch(c.applyLatest)
	}
	return e
}
func (c *Controller) SetMode(mode string, tun bool) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	if mode != "rule" && mode != "global" {
		return errors.New("模式无效")
	}
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	c.saved.Mode = mode
	c.saved.TUN = tun
	c.view.Mode = mode
	c.view.TUNEnabled = tun
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	on := c.view.ConnectionIntent
	e := c.persistLocked()
	c.mu.Unlock()
	if e == nil && on {
		c.launch(c.applyLatest)
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
	if c.view.RunState == "connected" && c.appliedGeneration == c.generation {
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
	if c.view.SelectionPending || !usableSubscription(p) {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 135*time.Second)
	c.cancelApply = cancel
	c.view.RunState = "connecting"
	c.view.LastError = ""
	c.mu.Unlock()
	// Release the old transport before any candidate consumes another slot.
	e := c.engine.Stop()
	if e == nil && id == "" {
		e = errors.New("暂无可用节点，请稍后刷新或手动选择节点")
	}
	if e == nil {
		e = api.Drain(ctx, p.SubscriptionID, p.SelectionRevision)
	}
	if e == nil {
		e = api.WaitDrained(ctx, p.SubscriptionID, p.SelectionRevision)
	}
	if e == nil {
		e = c.engine.Apply(ctx, p, b, id, mode, tun, server)
	}
	if e != nil && !errors.Is(e, ErrUnauthorized) && ctx.Err() == nil {
		if state, err := api.Connections(ctx); errors.Is(err, ErrUnauthorized) {
			e = err
		} else if err == nil && state.Message != "" {
			e = errors.New(state.Message)
		}
	}
	cancel()
	if errors.Is(e, ErrUnauthorized) {
		_ = c.expireSession(api)
		return
	}
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
		c.view.ActualSubscriptionID = ""
		c.view.LastError = e.Error()
		c.view.ProcessedConfigRevision = p.Revision
	} else {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
		c.view.ActualNodeID = id
		c.view.ActualSubscriptionID = p.SubscriptionID
		c.view.RunState = "connected"
		c.recoveryReady = true
		c.suspended = false
		c.appliedGeneration = g
		c.view.LastError = ""
		c.beginMetricsLocked()
	}
	c.mu.Unlock()
	c.report()
}
func (c *Controller) report() {
	if !c.beginWork() {
		return
	}
	defer c.workers.Done()
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.mu.Lock()
	api := c.api
	s := c.view.Status
	c.mu.Unlock()
	if api == nil || c.workCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Second)
	defer cancel()
	if err := api.Report(ctx, s); errors.Is(err, ErrUnauthorized) {
		// Some callers hold routeMu until reporting finishes. Clean up in a
		// tracked worker so expiry cannot deadlock that route operation.
		c.launch(func() { _ = c.expireSession(api) })
		return
	}
	c.mu.Lock()
	if c.api == api && !c.closed {
		_ = c.persistLocked()
	}
	c.mu.Unlock()
}
func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		c.CancelLogin()
		c.mu.Lock()
		c.closed = true
		c.cancelWork()
		c.generation++
		c.cancelRecoveryLocked()
		c.selectionGeneration++
		if c.cancelSelection != nil {
			c.cancelSelection()
		}
		if c.cancelApply != nil {
			c.cancelApply()
		}
		c.view.ConnectionIntent = false
		c.mu.Unlock()
		_ = c.engine.Stop()
		c.workers.Wait()
	})
}

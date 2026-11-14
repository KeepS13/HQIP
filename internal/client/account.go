package client

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Connection struct {
	SubscriptionID string    `json:"subscriptionId"`
	ID             string    `json:"id"`
	NodeID         string    `json:"nodeId"`
	State          string    `json:"state"`
	ExpiresAt      time.Time `json:"expiresAt"`
	LastError      string    `json:"lastError"`
}
type Connections struct {
	SubscriptionID     string       `json:"subscriptionId"`
	SelectionRevision  int64        `json:"selectionRevision"`
	Connections        []Connection `json:"connections"`
	Occupied           int          `json:"occupied"`
	MaxConnections     int          `json:"maxConnections"`
	AdmissionErrorCode string       `json:"admissionErrorCode"`
	Message            string       `json:"message"`
}

func connectionConfigurationChanged(old, next Profile, selected string) bool {
	if old.SubscriptionID != next.SubscriptionID || old.Rules.Hash != next.Rules.Hash {
		return true
	}
	var before, after Node
	for _, n := range old.Nodes {
		if n.ID == selected {
			before = n
		}
	}
	for _, n := range next.Nodes {
		if n.ID == selected {
			after = n
		}
	}
	// Readiness is health telemetry, not a new transport configuration.
	before.Ready, after.Ready = false, false
	return after.ID == "" || before != after
}

func (a *API) Connections(ctx context.Context) (Connections, error) {
	b, _, _, err := a.auth(ctx, "GET", "/api/v3/client/connections", nil, "")
	var v Connections
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	return v, err
}
func (a *API) Drain(ctx context.Context, subscriptionID string, revision int64) error {
	_, _, _, err := a.auth(ctx, "POST", "/api/v3/client/connections/drain", map[string]any{"subscriptionId": subscriptionID, "expectedRevision": revision}, "")
	return err
}
func (a *API) WaitDrained(ctx context.Context, subscriptionID string, revision int64) error {
	for {
		v, err := a.Connections(ctx)
		if err != nil {
			return err
		}
		if v.SubscriptionID != subscriptionID || v.SelectionRevision != revision {
			return ErrConflict
		}
		pending := false
		for _, c := range v.Connections {
			if c.SubscriptionID == subscriptionID && c.State != "closed" && c.State != "expired" && c.State != "rejected" {
				pending = true
			}
		}
		if !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("旧连接尚未确认关闭，请稍后手动重连")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// BrowserLogin only accepts callbacks on an ephemeral loopback listener. The
// browser sees a one-time code; account tokens are exchanged through direct TLS.
func (a *API) BrowserLogin(ctx context.Context, installationID, clientName string, open func(string) error) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/callback"
	state, verifier := randomID()+randomID(), randomID()+randomID()
	hash := sha256.Sum256([]byte(verifier))
	input := map[string]string{"codeChallenge": base64.RawURLEncoding.EncodeToString(hash[:]), "redirectUri": redirect, "state": state, "clientName": clientName, "installationId": installationID}
	raw, _, _, err := a.request(ctx, "POST", "/api/v3/client/authorize", input, "", "")
	if err != nil {
		return err
	}
	var auth struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if err = json.Unmarshal(raw, &auth); err != nil {
		return err
	}
	origin, _ := url.Parse(a.base)
	destination, e := url.Parse(auth.AuthorizationURL)
	if e != nil || !trustedAuthorizationOrigin(origin, destination) || destination.User != nil || destination.Fragment != "" || destination.Opaque != "" {
		return errors.New("拒绝跳转到不受信任的授权页面")
	}
	result := make(chan string, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		code := r.URL.Query().Get("code")
		if r.Host != listener.Addr().String() || subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 || len(code) < 16 || len(code) > 256 {
			http.Error(w, "Invalid login callback", http.StatusBadRequest)
			return
		}
		accepted := false
		once.Do(func() { result <- code; accepted = true })
		if !accepted {
			http.Error(w, "Callback already used", http.StatusConflict)
			return
		}
		writeLoginCallbackPage(w)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	if err = open(destination.String()); err != nil {
		return err
	}
	var code string
	select {
	case <-ctx.Done():
		return errors.New("登录已取消或超时，请重新登录")
	case code = <-result:
	}
	raw, _, _, err = a.request(ctx, "POST", "/api/v3/client/token", map[string]string{"code": code, "codeVerifier": verifier, "redirectUri": redirect, "installationId": installationID}, "", "")
	if err != nil {
		return err
	}
	var tokens Tokens
	if err = json.Unmarshal(raw, &tokens); err != nil {
		return err
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.SessionID == "" {
		return errors.New("账户服务未返回完整登录凭据")
	}
	a.mu.Lock()
	a.tokens = tokens
	a.mu.Unlock()
	return nil
}

func (c *Controller) LoginBrowser(server string, open func(string) error) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	a, err := NewAPI(strings.TrimRight(server, "/"), Tokens{})
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed || c.view.LoggedIn || c.view.LoginPending {
		c.mu.Unlock()
		return errors.New("已有登录会话或正在等待浏览器，请先退出或取消")
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Minute)
	c.loginGeneration++
	generation := c.loginGeneration
	c.authCancel = cancel
	c.view.LoginPending = true
	id := c.saved.InstallationID
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		if c.loginGeneration == generation {
			c.authCancel = nil
			c.view.LoginPending = false
		}
		c.mu.Unlock()
	}()
	name, _ := os.Hostname()
	if err = a.BrowserLogin(ctx, id, name, open); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed || c.loginGeneration != generation || ctx.Err() != nil {
		c.mu.Unlock()
		return errors.New("登录已取消")
	}
	c.api = a
	c.bindTokenPersistence(a)
	c.saved.ServerURL = a.base
	c.saved.AccountVersion = 3
	c.saved.Tokens = a.Tokens()
	c.view.ServerURL = a.base
	c.view.LoggedIn = true
	c.restoreSelection = true
	c.etag = ""
	err = c.persistLocked()
	c.mu.Unlock()
	if err != nil {
		_ = c.logoutFor(a)
		return err
	}
	return c.Sync(ctx)
}
func (c *Controller) CancelLogin() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loginGeneration++
	if c.authCancel != nil {
		c.authCancel()
		c.authCancel = nil
	}
	c.view.LoginPending = false
}
func (c *Controller) cacheUnavailable(api *API, p Profile, expected ...uint64) error {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if (len(expected) > 0 && expected[0] != c.generation) || c.api != api || c.view.SelectionPending || p.SelectionRevision < c.saved.Profile.SelectionRevision || (p.SubscriptionID == c.saved.Profile.SubscriptionID && p.Revision < c.saved.Profile.Revision) {
		c.mu.Unlock()
		return nil
	}
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	wasOn := c.view.ConnectionIntent || c.view.RunState == "connected"
	c.view.ConnectionIntent = false
	c.view.ActualNodeID = ""
	c.view.ActualSubscriptionID = ""
	if c.view.RunState != "failed" {
		c.view.RunState = "cached"
	}
	c.saved.Profile = p
	c.saved.SelectedSubscriptionID = p.SubscriptionID
	c.view.DesiredSubscriptionID = p.SubscriptionID
	c.populateLocked(p)
	c.saved.SelectedNodeID = p.DefaultNodeID
	c.view.DesiredNodeID = p.DefaultNodeID
	now := time.Now()
	c.view.LastSync = &now
	c.view.SyncError = ""
	c.view.ProcessedConfigRevision = p.Revision
	if wasOn {
		c.view.RunState = "failed"
		if failure := subscriptionFailure(p, c.clockNow()); failure != nil {
			c.view.LastError = failure.Error()
		}
	}
	err := c.persistLocked()
	c.mu.Unlock()
	_ = c.engine.Stop()
	return err
}
func (c *Controller) checkAccountConnection(ctx context.Context) {
	c.mu.Lock()
	api, selection, generation := c.api, c.selectionGeneration, c.generation
	c.mu.Unlock()
	if api == nil {
		return
	}
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := api.Connections(timeout)
	if errors.Is(err, ErrUnauthorized) {
		_ = c.expireSession(api)
		return
	}
	c.mu.Lock()
	current := c.api == api && c.selectionGeneration == selection && c.generation == generation && !c.view.SelectionPending && !c.closed
	if !current {
		c.mu.Unlock()
		return
	}
	if err != nil {
		if retryable(err) {
			c.startRecoveryLocked("lease_query_unavailable")
		}
		c.mu.Unlock()
		if !retryable(err) && !errors.Is(err, context.Canceled) {
			c.failGeneration(generation, err.Error())
		}
		return
	}
	if state.SubscriptionID == c.saved.SelectedSubscriptionID && state.SelectionRevision == c.saved.Profile.SelectionRevision {
		c.view.Connections = state
		if connectionRunning(c.view.RunState) && !matchingLease(state, c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID, c.clockNow()) {
			c.startRecoveryLocked("lease_missing")
		}
	} else if connectionRunning(c.view.RunState) {
		c.startRecoveryLocked("lease_selection_changed")
	}
	c.mu.Unlock()
}

// Independent of account polling/backoff, enforce authorization freshness locally.
func (c *Controller) accountWatchdog(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.accountWatchdogTick(ctx)
		}
	}
}

func (c *Controller) accountWatchdogTick(ctx context.Context) {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if !c.view.ConnectionIntent || c.closed {
		c.mu.Unlock()
		return
	}
	now := c.clockNow()
	selected := c.view.Subscription
	if c.view.SelectionPending {
		selected = nil
		for _, item := range c.view.Subscriptions {
			if item.ID == c.view.DesiredSubscriptionID {
				selected = &item
				break
			}
		}
	}
	message := ""
	if selected != nil && !selected.ExpiresAt.After(now) {
		message = "当前订阅已到期，VPN 已关闭，请手动选择其他订阅"
	}
	stale := c.view.LastSync == nil || now.Sub(*c.view.LastSync) >= 120*time.Second
	if stale && !c.recoveryReady && message == "" {
		message = "账户服务失联超过 120 秒，VPN 已关闭，请恢复后手动重连"
	}
	if message != "" {
		c.stopStateLocked(message)
		c.mu.Unlock()
		_ = c.engine.Stop()
		return
	}
	if !stale || c.suspended {
		c.mu.Unlock()
		return
	}
	c.startRecoveryLocked("authorization_stale")
	r := c.recovery
	c.mu.Unlock()
	if r == nil {
		return
	}
	err := c.engine.SetSuspended(r.ctx, true)
	c.mu.Lock()
	current := c.recoveryCurrentLocked(r)
	if current {
		if err == nil {
			c.suspended = true
		} else {
			c.stopStateLocked("暂停代理转发失败，VPN 已关闭，请手动重新连接")
		}
	}
	c.mu.Unlock()
	if current && err != nil {
		_ = c.engine.Stop()
	}
}

// Official deployments separate API and browser origins. Other servers retain same-origin authorization.
func trustedAuthorizationOrigin(api, page *url.URL) bool {
	if api == nil || page == nil || api.User != nil || page.User != nil || api.Opaque != "" || page.Opaque != "" || page.Fragment != "" {
		return false
	}
	if page.Scheme == api.Scheme && strings.EqualFold(page.Host, api.Host) {
		return true
	}
	if api.Scheme != "https" || page.Scheme != "https" || page.Path != "/authorize" || page.RawPath != "" {
		return false
	}
	return strings.EqualFold(api.Host, "api-hqip.sharevps.online") && strings.EqualFold(page.Host, "hqip.sharevps.online")
}

func AccountURL(server string) string {
	if migrateOfficialServerURL(server) == DefaultServerURL {
		return UserSiteURL + "/subscriptions"
	}
	return strings.TrimRight(server, "/") + "/subscriptions"
}

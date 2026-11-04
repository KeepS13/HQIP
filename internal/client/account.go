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
	ID        string    `json:"id"`
	NodeID    string    `json:"nodeId"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expiresAt"`
	LastError string    `json:"lastError"`
}
type Connections struct {
	Connections        []Connection `json:"connections"`
	Occupied           int          `json:"occupied"`
	MaxConnections     int          `json:"maxConnections"`
	AdmissionErrorCode string       `json:"admissionErrorCode"`
	Message            string       `json:"message"`
}

func connectionConfigurationChanged(old, next Profile, selected string) bool {
	if old.Rules.Hash != next.Rules.Hash {
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
	return after.ID == "" || before != after
}

func (a *API) Connections(ctx context.Context) (Connections, error) {
	b, _, _, err := a.auth(ctx, "GET", "/api/v2/client/connections", nil, "")
	var v Connections
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	return v, err
}
func (a *API) Drain(ctx context.Context) error {
	_, _, _, err := a.auth(ctx, "POST", "/api/v2/client/connections/drain", map[string]any{}, "")
	return err
}
func (a *API) WaitDrained(ctx context.Context) error {
	for {
		v, err := a.Connections(ctx)
		if err != nil {
			return err
		}
		pending := false
		for _, c := range v.Connections {
			if c.State != "closed" && c.State != "expired" && c.State != "rejected" {
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
	raw, _, _, err := a.request(ctx, "POST", "/api/v2/client/authorize", input, "", "")
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
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("登录授权已送达 HQIP，请返回客户端查看结果。可以关闭此页面。"))
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
	raw, _, _, err = a.request(ctx, "POST", "/api/v2/client/token", map[string]string{"code": code, "codeVerifier": verifier, "redirectUri": redirect, "installationId": installationID}, "", "")
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
	a, err := NewAPI(strings.TrimRight(server, "/"), Tokens{})
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed || c.view.LoggedIn || c.view.LoginPending {
		c.mu.Unlock()
		return errors.New("已有登录会话或正在等待浏览器，请先退出或取消")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	c.saved.ServerURL = a.base
	c.saved.AccountVersion = 2
	c.saved.Tokens = a.Tokens()
	c.view.ServerURL = a.base
	c.view.LoggedIn = true
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
func (c *Controller) cacheUnavailable(api *API, p Profile) error {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.api != api || p.Revision < c.saved.Profile.Revision {
		c.mu.Unlock()
		return nil
	}
	c.generation++
	if c.cancelApply != nil {
		c.cancelApply()
	}
	wasOn := c.view.ConnectionIntent || c.view.RunState == "connected"
	c.view.ConnectionIntent = false
	c.view.ActualNodeID = ""
	if c.view.RunState != "failed" {
		c.view.RunState = "cached"
	}
	c.saved.Profile = p
	c.populateLocked(p)
	c.saved.SelectedNodeID = p.DefaultNodeID
	c.view.DesiredNodeID = p.DefaultNodeID
	now := time.Now()
	c.view.LastSync = &now
	c.view.SyncError = ""
	c.view.ProcessedConfigRevision = p.Revision
	if wasOn {
		c.view.RunState = "failed"
		c.view.LastError = "套餐未开通、已到期或流量已用尽，VPN 已关闭"
	}
	err := c.persistLocked()
	c.mu.Unlock()
	_ = c.engine.Stop()
	return err
}
func (c *Controller) checkAccountConnection(ctx context.Context) {
	c.mu.Lock()
	api := c.api
	c.mu.Unlock()
	if api == nil {
		return
	}
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := api.Connections(timeout)
	if err != nil {
		return
	}
	c.routeMu.Lock()
	c.mu.Lock()
	stop := false
	if c.api == api {
		c.view.Connections = state
		if c.view.RunState == "connected" {
			active := false
			for _, lease := range state.Connections {
				if lease.State == "active" && lease.ExpiresAt.After(time.Now()) {
					active = true
				}
			}
			if !active {
				c.generation++
				if c.cancelApply != nil {
					c.cancelApply()
				}
				c.view.ConnectionIntent = false
				c.view.ActualNodeID = ""
				c.view.RunState = "failed"
				c.view.LastError = "服务器连接授权已结束，VPN 已关闭，请检查套餐或在线连接后手动重连"
				stop = true
			}
		}
	}
	c.mu.Unlock()
	if stop {
		_ = c.engine.Stop()
	}
	c.routeMu.Unlock()
}

// Polling/backoff may be slow, so the local stop deadline has an independent
// clock. The server core also enforces its own shorter-or-equal lease deadline.
func (c *Controller) accountWatchdog(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.routeMu.Lock()
		c.mu.Lock()
		message := ""
		if c.view.ConnectionIntent {
			if c.view.LastSync == nil || time.Since(*c.view.LastSync) >= 120*time.Second {
				message = "账户服务失联超过 120 秒，VPN 已关闭，请恢复后手动重连"
			}
			if c.view.Entitlement != nil && !c.view.Entitlement.ExpiresAt.After(time.Now()) {
				message = "套餐已到期，VPN 已关闭"
			}
		}
		if message != "" {
			c.generation++
			if c.cancelApply != nil {
				c.cancelApply()
			}
			c.view.ConnectionIntent = false
			c.view.ActualNodeID = ""
			c.view.RunState = "failed"
			c.view.LastError = message
		}
		c.mu.Unlock()
		if message != "" {
			_ = c.engine.Stop()
		}
		c.routeMu.Unlock()
	}
}

// Official deployments separate API and browser origins. Other servers retain same-origin authorization.
func trustedAuthorizationOrigin(api, page *url.URL) bool {
	if api == nil || page == nil {
		return false
	}
	if page.Scheme == api.Scheme && strings.EqualFold(page.Host, api.Host) {
		return true
	}
	return api.Scheme == "https" && strings.EqualFold(api.Host, "api-hqip.5202345.xyz") && page.Scheme == "https" && strings.EqualFold(page.Host, "hqip.5202345.xyz") && page.Path == "/authorize"
}

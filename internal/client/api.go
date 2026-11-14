package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrUnauthorized = errors.New("用户登录已失效，请重新登录")
var ErrConflict = errors.New("订阅选择已变化，请刷新后重试")

type API struct {
	mu     sync.Mutex
	authMu sync.Mutex // Serialize token rotation without blocking local snapshots.
	base   string
	tokens Tokens
	http   *http.Client
}

func NewAPI(base string, tokens Tokens) (*API, error) {
	u, e := url.Parse(strings.TrimRight(base, "/"))
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("服务地址必须是 HTTPS 根地址")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return nil, errors.New("远程服务必须使用 HTTPS")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &API{base: u.String(), tokens: tokens, http: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *API) Tokens() Tokens { a.mu.Lock(); defer a.mu.Unlock(); return a.tokens }
func (a *API) request(ctx context.Context, method, path string, body any, token, etag string) ([]byte, string, int, error) {
	if !strings.HasPrefix(path, "/api/v3/client/") || strings.Contains(path, "..") {
		return nil, "", 0, errors.New("拒绝非客户端接口")
	}
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, "", 0, e
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, a.base+path, reader)
	if e != nil {
		return nil, "", 0, e
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, e := a.http.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, "", 0, ctx.Err()
		}
		var cert *tls.CertificateVerificationError
		if errors.As(e, &cert) {
			return nil, "", 0, errors.New("账户服务 HTTPS 证书校验失败")
		}
		return nil, "", 0, temporary(errors.New("无法连接账户服务，请检查网络或稍后重试"))
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if e != nil {
		return nil, "", resp.StatusCode, temporary(errors.New("账户服务响应中断"))
	}
	// Native authentication failures are 401. A 403 can be a subscription
	// entitlement failure and must not discard an otherwise valid login.
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, "", resp.StatusCode, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusConflict {
		return nil, "", resp.StatusCode, ErrConflict
	}
	if resp.StatusCode != 304 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		var msg struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &msg)
		if msg.Error == "" {
			msg.Error = fmt.Sprintf("服务返回 %d", resp.StatusCode)
		}
		err := errors.New(msg.Error)
		if resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			err = temporary(err)
		}
		return nil, "", resp.StatusCode, err
	}
	return b, resp.Header.Get("ETag"), resp.StatusCode, nil
}

func (a *API) auth(ctx context.Context, method, path string, body any, etag string) ([]byte, string, int, error) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	tokens := a.Tokens()
	if tokens.RefreshToken == "" {
		return nil, "", 401, ErrUnauthorized
	}
	if time.Until(tokens.AccessExpiresAt) < time.Minute {
		if e := a.refresh(ctx); e != nil {
			return nil, "", 401, e
		}
	}
	b, t, s, e := a.request(ctx, method, path, body, a.Tokens().AccessToken, etag)
	if errors.Is(e, ErrUnauthorized) {
		if e = a.refresh(ctx); e != nil {
			return nil, "", 401, e
		}
		return a.request(ctx, method, path, body, a.Tokens().AccessToken, etag)
	}
	return b, t, s, e
}
func (a *API) refresh(ctx context.Context) error {
	b, _, _, e := a.request(ctx, "POST", "/api/v3/client/refresh", map[string]string{"refreshToken": a.Tokens().RefreshToken}, "", "")
	if e != nil {
		return e
	}
	var tokens Tokens
	if e = json.Unmarshal(b, &tokens); e != nil {
		return e
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.SessionID == "" {
		return errors.New("账户服务未返回完整刷新凭据")
	}
	a.mu.Lock()
	a.tokens = tokens
	a.mu.Unlock()
	return nil
}
func (a *API) Profile(ctx context.Context, etag string) (Profile, string, bool, error) {
	b, t, s, e := a.auth(ctx, "GET", "/api/v3/client/profile", nil, etag)
	var p Profile
	if e == nil && s != 304 {
		e = json.Unmarshal(b, &p)
	}
	return p, t, s == 304, e
}
func (a *API) SelectSubscription(ctx context.Context, id string, revision int64) (Profile, error) {
	b, _, _, err := a.auth(ctx, "POST", "/api/v3/client/subscription", map[string]any{"subscriptionId": id, "expectedRevision": revision}, "")
	var p Profile
	if err == nil {
		err = json.Unmarshal(b, &p)
	}
	return p, err
}
func (a *API) Rules(ctx context.Context, path string) ([]byte, error) {
	// Profiles carry an absolute rules URL. Only the configured controller may
	// receive this device's bearer token, and only its immutable rules resource.
	u, err := url.Parse(path)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return nil, errors.New("规则下载地址无效")
	}
	if u.IsAbs() || u.Host != "" {
		base, _ := url.Parse(a.base)
		if u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
			return nil, errors.New("规则下载地址不属于当前管理服务")
		}
	}
	const prefix = "/api/v3/client/rules/"
	if !strings.HasPrefix(u.Path, prefix) {
		return nil, errors.New("规则下载地址无效")
	}
	hash := strings.TrimPrefix(u.Path, prefix)
	if _, err = hex.DecodeString(hash); err != nil || len(hash) != 64 {
		return nil, errors.New("规则下载地址无效")
	}
	b, _, _, e := a.auth(ctx, "GET", u.Path, nil, "")
	return b, e
}
func (a *API) Report(ctx context.Context, s Status) error {
	_, _, _, e := a.auth(ctx, "POST", "/api/v3/client/status", s, "")
	return e
}
func (a *API) Logout(ctx context.Context) error {
	_, _, _, e := a.auth(ctx, "POST", "/api/v3/client/logout", nil, "")
	return e
}

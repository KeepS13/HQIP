package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
	"gopkg.in/yaml.v3"
)

func (s *Server) registerPortalAdmin(m *http.ServeMux) {
	s.registerPaymentAdmin(m)
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/subscription-templates": s.subscriptionTemplates, "POST /api/v1/subscription-templates": s.saveSubscriptionTemplate, "PUT /api/v1/subscription-templates/{id}": s.saveSubscriptionTemplate, "POST /api/v1/subscription-templates/{id}/preview": s.previewSubscriptionTemplate,
		"GET /api/v1/subscription-requests": s.adminSubscriptionRequests, "POST /api/v1/subscription-requests/{id}/approve": stoppedApplication, "POST /api/v1/subscription-requests/{id}/reject": stoppedApplication,
		"GET /api/v1/users/{id}/subscriptions": s.adminUserSubscriptions, "POST /api/v1/users/{id}/subscriptions": s.adminAssignSubscription, "DELETE /api/v1/users/{id}/subscriptions/{subscriptionId}": s.adminRemoveSubscription,
		"GET /api/v1/users": s.portalUsers, "GET /api/v1/users/{id}": s.portalAdminUser, "PATCH /api/v1/users/{id}": s.portalAdminEnabled, "POST /api/v1/users/{id}/password": s.portalAdminPassword,
		"POST /api/v1/users/{id}/sessions/{sessionId}/revoke": s.portalAdminRevokeSession, "POST /api/v1/users/{id}/connections/{leaseId}/revoke": s.portalAdminRevokeConnection,
		"GET /api/v1/settings/turnstile": s.portalTurnstileSettings, "PUT /api/v1/settings/turnstile": s.portalTurnstileSettings, "GET /api/v1/settings/portal": s.portalSettings, "PUT /api/v1/settings/portal": s.portalSettings, "POST /api/v1/settings/portal/activate": s.portalActivate,
		"GET /api/v1/settings/portal/recovery": s.portalRecovery, "POST /api/v1/settings/portal/recovery/resume": s.portalResume,
	} {
		m.Handle(pattern, s.auth(handler, false))
	}
}
func (s *Server) registerPortalPublic(m *http.ServeMux) {
	s.registerPaymentPublic(m)
	m.HandleFunc("GET /api/v1/user/config", s.portalConfig)
	m.HandleFunc("POST /api/v1/user/register", s.portalRegister)
	m.HandleFunc("POST /api/v1/user/login", s.portalLogin)
	m.HandleFunc("GET /api/v1/user/session", s.portalSession)
	m.HandleFunc("GET /api/v1/user/releases", s.portalReleases)
	m.HandleFunc("GET /api/v1/user/download", s.portalDownloadInfo)
	m.HandleFunc("GET /downloads/HQIP-Windows-x64.zip", s.portalDownload)
	m.HandleFunc("GET /downloads/{sha256}/HQIP-Windows-x64.zip", s.portalDownload)
	for pattern, handler := range map[string]portalHandler{
		"GET /api/v1/user/profile": s.portalProfile, "POST /api/v1/user/logout": s.portalLogout, "POST /api/v1/user/password": s.portalPassword,
		"GET /api/v1/user/catalog": s.userCatalog, "GET /api/v1/user/subscription-requests": s.userSubscriptionRequests,
		"GET /api/v1/user/subscriptions": s.userSubscriptions, "GET /api/v1/user/subscriptions/{id}": s.userSubscriptionDetail,
		"POST /api/v1/user/subscriptions/{id}/import-link": s.portalSubscription, "POST /api/v1/user/subscriptions/{id}/import-link/rotate": s.portalSubscription, "POST /api/v1/user/subscriptions/{id}/import-link/revoke": s.portalSubscription,
		"GET /api/v1/user/sessions": s.portalSessions, "POST /api/v1/user/sessions/{id}/revoke": s.portalRevokeSession,
		"GET /api/v1/user/authorize/{id}": s.portalAuthorize, "POST /api/v1/user/authorize/{id}": s.portalAuthorize,
	} {
		m.Handle(pattern, s.portalAuth("browser", handler))
	}
	m.HandleFunc("GET /usub/{token}", s.portalYAML)
	m.HandleFunc("/api/v2/client/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusUpgradeRequired, "请升级 HQIP 多订阅客户端后重新登录")
	})
	m.HandleFunc("POST /api/v3/client/authorize", s.nativeBegin)
	m.HandleFunc("POST /api/v3/client/token", s.nativeToken)
	m.HandleFunc("POST /api/v3/client/refresh", s.nativeRefresh)
	for pattern, handler := range map[string]portalHandler{
		"POST /api/v3/client/logout": s.portalLogout, "GET /api/v3/client/profile": s.nativeProfile, "POST /api/v3/client/subscription": s.nativeSelectSubscription, "POST /api/v3/client/status": s.nativeStatus, "GET /api/v3/client/rules/{hash}": s.nativeRules,
		"GET /api/v3/client/connections": s.portalConnections, "POST /api/v3/client/connections/drain": s.nativeDrain,
	} {
		m.Handle(pattern, s.portalAuth("native", handler))
	}
	m.HandleFunc("POST /api/v1/agents/admission", s.agentAdmission)
	m.HandleFunc("POST /api/v1/agents/leases/{id}/renew", s.agentLeaseRenew)
}

type portalHandler func(http.ResponseWriter, *http.Request, string, string)

func (s *Server) portalOrigin(r *http.Request) bool {
	got, err := url.Parse(r.Header.Get("Origin"))
	want, e := url.Parse(s.opts.UserOrigin)
	return err == nil && e == nil && got.Scheme == want.Scheme && got.Host == want.Host && got.Path == "" && got.RawQuery == "" && got.Fragment == "" && got.User == nil
}
func portalCookie(r *http.Request) string {
	c, e := r.Cookie("hqip_user")
	if e != nil {
		return ""
	}
	return c.Value
}
func (s *Server) setPortalCookie(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "hqip_user", Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.opts.PublicURL, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s *Server) portalAuth(kind string, next portalHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := portalCookie(r)
		if kind == "native" {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(r.Header.Get("Authorization")) != 50 {
				fail(w, 401, "用户登录已失效")
				return
			}
			token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		user, session, csrf, err := s.Store.PortalIdentity(token, kind)
		if err != nil {
			fail(w, 401, "用户登录已失效")
			return
		}
		if kind == "browser" && r.Method != "GET" && r.Method != "HEAD" && (!s.portalOrigin(r) || !secure.Equal(csrf, r.Header.Get("X-CSRF-Token"))) {
			fail(w, 403, "安全校验失败")
			return
		}
		next(w, r, user, session)
	})
}
func portalResult(w http.ResponseWriter, v any, err error) {
	if err == nil {
		reply(w, 200, v)
		return
	}
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		fail(w, 401, "登录或连接凭据无效")
	case errors.Is(err, store.ErrNotFound):
		fail(w, 404, "记录不存在")
	case errors.Is(err, store.ErrSlots), errors.Is(err, store.ErrConflict):
		fail(w, 409, err.Error())
	case errors.Is(err, store.ErrEntitlement):
		fail(w, 403, err.Error())
	default:
		fail(w, 400, "操作失败，请检查输入或稍后重试")
	}
}
func (s *Server) portalConfig(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Store.TurnstileSettings()
	reply(w, 200, map[string]any{"registrationEnabled": s.Store.PortalSetting("portal_registration") == "true", "portalEnabled": s.Store.PortalEnabled(), "recoveryHold": s.Store.PortalSetting("portal_recovery_hold") == "true", "turnstileSiteKey": cfg.SiteKey, "turnstileConfigured": cfg.SecretConfigured})
}
func (s *Server) portalSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" {
		var b struct {
			RegistrationEnabled bool `json:"registrationEnabled"`
		}
		if !readJSON(w, r, &b) {
			return
		}
		value := "false"
		if b.RegistrationEnabled {
			value = "true"
		}
		if err := s.Store.SetPortalSetting("portal_registration", value); err != nil {
			portalResult(w, nil, err)
			return
		}
	}
	s.portalConfig(w, r)
}
func (s *Server) portalTurnstileSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" {
		var b struct {
			SiteKey          string   `json:"siteKey"`
			SecretKey        *string  `json:"secretKey"`
			AllowedHostnames []string `json:"allowedHostnames"`
			ExpectedRevision int64    `json:"expectedRevision"`
		}
		if !readJSON(w, r, &b) {
			return
		}
		if len(b.AllowedHostnames) == 0 {
			base, _ := url.Parse(s.opts.UserOrigin)
			b.AllowedHostnames = []string{base.Hostname()}
		}
		v, err := s.Store.SaveTurnstile(b.SiteKey, b.AllowedHostnames, b.SecretKey, b.ExpectedRevision)
		portalResult(w, v, err)
		return
	}
	v, e := s.Store.TurnstileSettings()
	portalResult(w, v, e)
}
func (s *Server) verifyTurnstile(ctx context.Context, token, ip, action string) bool {
	if len(token) < 1 || len(token) > 2048 {
		return false
	}
	settings, secret, err := s.Store.TurnstileVerificationConfig()
	if err != nil || secret == "" || settings.SiteKey == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	form := url.Values{"secret": {secret}, "response": {token}, "remoteip": {ip}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := s.opts.TurnstileClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false
	}
	var answer struct {
		Success     bool      `json:"success"`
		Hostname    string    `json:"hostname"`
		Action      string    `json:"action"`
		ChallengeTS time.Time `json:"challenge_ts"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&answer) != nil {
		return false
	}
	base, _ := url.Parse(s.opts.UserOrigin)
	now := s.Store.Now()
	allowed := settings.AllowedHostnames
	if len(allowed) == 0 {
		allowed = []string{base.Hostname()}
	}
	hostOK := false
	for _, h := range allowed {
		if strings.EqualFold(h, answer.Hostname) {
			hostOK = true
		}
	}
	if !answer.Success || !hostOK || answer.Action != action || answer.ChallengeTS.Before(now.Add(-5*time.Minute)) || answer.ChallengeTS.After(now.Add(time.Minute)) {
		return false
	}
	return s.Store.ConsumeTurnstile(token)
}

type portalLoginBody struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	TurnstileToken string `json:"turnstileToken"`
}

func (s *Server) portalLoginAttempt(w http.ResponseWriter, r *http.Request, action string) (portalLoginBody, bool) {
	var b portalLoginBody
	if !s.portalOrigin(r) {
		fail(w, 403, "请求来源无效")
		return b, false
	}
	if !s.allowLogin(w, r, "portal:"+clientIP(r)) {
		return b, false
	}
	if !readJSON(w, r, &b) {
		return b, false
	}
	if len(b.Email) > 254 || len(b.Password) > 1024 {
		fail(w, 400, "邮箱或密码格式无效")
		return b, false
	}
	if !s.allowLogin(w, r, "portal-account:"+secure.Hash(strings.ToLower(strings.TrimSpace(b.Email)))) {
		return b, false
	}
	if !s.verifyTurnstile(r.Context(), b.TurnstileToken, clientIP(r), action) {
		fail(w, 403, "人机验证失败，请重新验证")
		return b, false
	}
	return b, true
}
func (s *Server) portalRegister(w http.ResponseWriter, r *http.Request) {
	if s.Store.PortalSetting("portal_registration") != "true" {
		fail(w, 403, "注册尚未开放")
		return
	}
	b, ok := s.portalLoginAttempt(w, r, "register")
	if !ok {
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		fail(w, 429, "请求繁忙")
		return
	}
	if _, err := s.Store.RegisterPortal(b.Email, b.Password); err != nil {
		portalResult(w, nil, err)
		return
	}
	s.finishPortalLogin(w, b)
}
func (s *Server) portalLogin(w http.ResponseWriter, r *http.Request) {
	b, ok := s.portalLoginAttempt(w, r, "login")
	if !ok {
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		fail(w, 429, "请求繁忙")
		return
	}
	s.finishPortalLogin(w, b)
}
func (s *Server) finishPortalLogin(w http.ResponseWriter, b portalLoginBody) {
	t, csrf, err := s.Store.PortalLogin(b.Email, b.Password)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	s.setPortalCookie(w, t.AccessToken, 86400)
	reply(w, 200, map[string]any{"authenticated": true, "user": t.User, "csrfToken": csrf})
}
func (s *Server) portalSession(w http.ResponseWriter, r *http.Request) {
	user, _, csrf, err := s.Store.PortalIdentity(portalCookie(r), "browser")
	if err != nil {
		reply(w, 200, map[string]bool{"authenticated": false})
		return
	}
	u, err := s.Store.PortalUser(user)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	reply(w, 200, map[string]any{"authenticated": true, "user": u, "csrfToken": csrf})
}
func (s *Server) portalLogout(w http.ResponseWriter, r *http.Request, user, session string) {
	err := s.Store.RevokePortalSession(user, session)
	if strings.HasPrefix(r.URL.Path, "/api/v1/user/") {
		s.setPortalCookie(w, "", -1)
	}
	portalResult(w, map[string]bool{"ok": true}, err)
}
func (s *Server) portalPassword(w http.ResponseWriter, r *http.Request, user, session string) {
	if !s.beginPasswordWork(w, r, "user:"+user) {
		return
	}
	defer func() { <-s.authSlots }()
	var b struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	portalResult(w, map[string]bool{"ok": true}, s.Store.PortalPassword(user, b.Current, b.New, false))
}
func (s *Server) portalProfile(w http.ResponseWriter, r *http.Request, user, session string) {
	v, err := s.portalProfileData(user, session)
	portalResult(w, v, err)
}
func (s *Server) portalProfileData(user, session string) (map[string]any, error) {
	u, err := s.Store.PortalUser(user)
	if err != nil {
		return nil, err
	}
	sessions, err := s.Store.PortalSessions(user, session)
	if err != nil {
		return nil, err
	}
	v := map[string]any{"user": u, "subscriptions": u.Subscriptions, "sessions": sessions}
	if session == "" { // Only the authenticated administrator view includes VPN history.
		connections, err := s.Store.PortalConnections(user, "")
		if err != nil {
			return nil, err
		}
		v["connections"] = connections
	}
	return v, nil
}
func (s *Server) portalSubscription(w http.ResponseWriter, r *http.Request, user, session string) {
	action := "create"
	if strings.HasSuffix(r.URL.Path, "/rotate") {
		action = "rotate"
	}
	if strings.HasSuffix(r.URL.Path, "/revoke") {
		action = "revoke"
	}
	link, err := s.Store.PortalSubscription(user, r.PathValue("id"), action)
	portalResult(w, map[string]string{"url": link}, err)
}
func (s *Server) portalSessions(w http.ResponseWriter, r *http.Request, user, session string) {
	v, e := s.Store.PortalSessions(user, session)
	portalResult(w, v, e)
}
func (s *Server) portalRevokeSession(w http.ResponseWriter, r *http.Request, user, session string) {
	portalResult(w, map[string]bool{"ok": true}, s.Store.RevokePortalSession(user, r.PathValue("id")))
}
func (s *Server) portalConnections(w http.ResponseWriter, r *http.Request, user, session string) {
	v, err := s.Store.PortalConnections(user, session)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	id, selectionRevision, err := s.Store.SelectedSubscription(user, session)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	var e *model.UserSubscription
	if id != "" {
		e, err = s.Store.UserSubscription(user, id)
		if err != nil {
			portalResult(w, nil, err)
			return
		}
	}
	max, occupied := 0, 0
	if e != nil {
		max = e.MaxConnections
		occupied = e.Occupied
	}
	code, message := "", ""
	if e == nil || e.Status != "active" {
		code = "SUBSCRIPTION_UNAVAILABLE"
		message = "请选择有效订阅；当前订阅可能已到期、移除或流量耗尽"
	} else if occupied >= max {
		code = "CONNECTION_LIMIT"
		message = "在线连接数已达此订阅上限，请先断开使用此订阅的其他连接"
	}
	reply(w, 200, map[string]any{"subscriptionId": id, "selectionRevision": selectionRevision, "connections": v, "maxConnections": max, "occupied": occupied, "admissionErrorCode": code, "message": message})
}
func (s *Server) portalAuthorize(w http.ResponseWriter, r *http.Request, user, session string) {
	if r.Method == "GET" {
		name, expires, err := s.Store.PortalAuthorization(r.PathValue("id"))
		portalResult(w, map[string]any{"clientName": name, "expiresAt": expires}, err)
		return
	}
	redirect, err := s.Store.ApprovePortalAuthorization(r.PathValue("id"), user, session)
	portalResult(w, map[string]string{"redirectUrl": redirect}, err)
}
func (s *Server) nativeBegin(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(w, r, "native-authorize:"+clientIP(r)) {
		return
	}
	var b struct {
		CodeChallenge  string `json:"codeChallenge"`
		RedirectURI    string `json:"redirectUri"`
		State          string `json:"state"`
		ClientName     string `json:"clientName"`
		InstallationID string `json:"installationId"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	id, expires, err := s.Store.BeginPortalAuthorization(b.CodeChallenge, b.RedirectURI, b.State, b.ClientName, b.InstallationID)
	portalResult(w, map[string]any{"requestId": id, "authorizationUrl": s.opts.UserOrigin + "/authorize?request=" + id, "expiresAt": expires}, err)
}
func (s *Server) nativeToken(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(w, r, "native-token:"+clientIP(r)) {
		return
	}
	var b struct {
		Code           string `json:"code"`
		CodeVerifier   string `json:"codeVerifier"`
		RedirectURI    string `json:"redirectUri"`
		InstallationID string `json:"installationId"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	t, e := s.Store.ExchangePortalCode(b.Code, b.CodeVerifier, b.RedirectURI, b.InstallationID)
	portalResult(w, t, e)
}
func (s *Server) nativeRefresh(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	t, e := s.Store.PortalRefresh(b.RefreshToken)
	portalResult(w, t, e)
}
func (s *Server) nativeProfile(w http.ResponseWriter, r *http.Request, user, session string) {
	id, selectionRevision, err := s.Store.SelectedSubscription(user, session)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	p, e, err := s.Store.PortalClientProfile(user, session, id)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	u, err := s.Store.PortalUser(user)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	p.SubscriptionID = id
	p.SelectionRevision = selectionRevision
	availableNodes, err := s.Store.ClientNodeCatalog(u.Subscriptions)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	latestID, latestSelection, err := s.Store.SelectedSubscription(user, session)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	if latestID != id || latestSelection != selectionRevision {
		portalResult(w, nil, store.ErrConflict)
		return
	}
	v := struct {
		model.ClientProfile
		User           model.PortalUser           `json:"user"`
		Subscription   *model.UserSubscription    `json:"subscription"`
		Subscriptions  []model.UserSubscription   `json:"subscriptions"`
		AvailableNodes []model.ClientNodeEndpoint `json:"availableNodes"`
	}{p, u, e, u.Subscriptions, availableNodes}
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	etag := "\"" + hex.EncodeToString(sum[:]) + "\""
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	reply(w, 200, v)
}
func (s *Server) nativeStatus(w http.ResponseWriter, r *http.Request, user, session string) {
	var b model.ClientStatus
	if !readJSON(w, r, &b) {
		return
	}
	if len(b.LastError) > 2000 || len(b.CoreVersion) > 100 || len(b.RunState) > 40 {
		fail(w, 400, "状态字段过长")
		return
	}
	raw, _ := json.Marshal(b)
	_, err := s.Store.DB.Exec("UPDATE portal_sessions SET status=? WHERE id=? AND user_id=?", string(raw), session, user)
	portalResult(w, map[string]bool{"ok": true}, err)
}
func (s *Server) nativeRules(w http.ResponseWriter, r *http.Request, user, session string) {
	raw, err := s.Store.RuleBytes(r.PathValue("hash"))
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
func (s *Server) nativeDrain(w http.ResponseWriter, r *http.Request, user, session string) {
	var b struct {
		SubscriptionID   string `json:"subscriptionId"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	portalResult(w, map[string]bool{"ok": true}, s.Store.DrainSubscriptionSession(user, session, b.SubscriptionID, b.ExpectedRevision))
}
func (s *Server) portalYAML(w http.ResponseWriter, r *http.Request) {
	format, err := portalExportFormat(r.URL.RawQuery)
	if err != nil {
		fail(w, 400, "不支持的订阅格式")
		return
	}
	if !s.Store.PortalEnabled() {
		fail(w, 404, "订阅不可用")
		return
	}
	user, subscription, err := s.Store.ResolvePortalSubscription(r.PathValue("token"))
	if err != nil {
		fail(w, 404, "订阅不存在或已撤销")
		return
	}
	p, e, err := s.Store.PortalClientProfile(user, "subscription", subscription)
	if err != nil {
		currentUser, currentSubscription, tokenErr := s.Store.ResolvePortalSubscription(r.PathValue("token"))
		if tokenErr != nil || currentUser != user || currentSubscription != subscription {
			fail(w, 404, "订阅不存在或已撤销")
			return
		}
		portalResult(w, nil, err)
		return
	}
	if e == nil || e.Status != "active" {
		fail(w, 403, "此订阅已失效")
		return
	}
	proxies := []map[string]any{}
	names := []string{}
	defaultReady := false
	for _, n := range p.Nodes {
		if !n.Ready {
			continue
		}
		name := n.Name + " · " + n.ID
		proxy := map[string]any{"name": name, "type": "hysteria2", "server": n.Server, "port": n.Port, "password": n.Password, "sni": n.SNI, "skip-cert-verify": false}
		if n.ID == p.DefaultNodeID {
			defaultReady = true
			names = append([]string{name}, names...)
			proxies = append([]map[string]any{proxy}, proxies...)
		} else {
			names = append(names, name)
			proxies = append(proxies, proxy)
		}
	}
	if len(proxies) == 0 {
		fail(w, 503, "此订阅尚无可用节点")
		return
	}
	var body []byte
	contentType := "text/yaml; charset=utf-8"
	if format == "v2ray" {
		body, err = portalV2RaySubscription(p)
		contentType = "text/plain; charset=utf-8"
	} else {
		v, rulesErr := s.Store.Rules()
		if rulesErr != nil {
			portalResult(w, nil, rulesErr)
			return
		}
		rules, rulesErr := v.Bundle.ClashRules("节点选择", "DIRECT")
		if rulesErr != nil {
			portalResult(w, nil, rulesErr)
			return
		}
		groups := []map[string]any{}
		if !defaultReady {
			const unavailable = "默认节点不可用，请手动选择"
			names = append([]string{unavailable}, names...)
			groups = append(groups, map[string]any{"name": unavailable, "type": "select", "proxies": []string{"REJECT"}})
		}
		groups = append([]map[string]any{{"name": "节点选择", "type": "select", "proxies": names}}, groups...)
		body, err = yaml.Marshal(map[string]any{"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "warning", "proxies": proxies, "proxy-groups": groups, "rules": rules})
	}
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	// Profile construction can span a subscription rotation. Never deliver
	// replacement credentials to a request authenticated by the previous URL.
	current, currentSubscription, err := s.Store.ResolvePortalSubscription(r.PathValue("token"))
	if err != nil || current != user || currentSubscription != subscription {
		fail(w, 404, "订阅不存在或已撤销")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Profile-Update-Interval", "1")
	_, _ = w.Write(body)
}

package control

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

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
			if errors.Is(err, store.ErrUnauthorized) {
				fail(w, 401, "用户登录已失效")
			} else {
				w.Header().Set("Retry-After", "5")
				fail(w, 503, "账户服务暂时不可用，请稍后重试")
			}
			return
		}
		name := user
		if _, ok := r.Context().Value(operationContextKey{}).(*operationContext); ok {
			if profile, e := s.Store.PortalUser(user); e == nil {
				name = profile.Email
			}
			setOperationActor(r, "user", user, name, kind)
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
	case errors.Is(err, store.ErrSlots), errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrSubscriptionStock):
		fail(w, 409, err.Error())
	case errors.Is(err, store.ErrEntitlement):
		fail(w, 403, err.Error())
	default:
		fail(w, 400, "操作失败，请检查输入或稍后重试")
	}
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
	s.finishPortalLogin(w, r, b)
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
	s.finishPortalLogin(w, r, b)
}
func (s *Server) finishPortalLogin(w http.ResponseWriter, r *http.Request, b portalLoginBody) {
	t, csrf, err := s.Store.PortalLogin(b.Email, b.Password)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	s.setPortalCookie(w, t.AccessToken, 86400)
	setOperationActor(r, "user", t.User.ID, t.User.Email, "password")
	reply(w, 200, map[string]any{"authenticated": true, "user": t.User, "csrfToken": csrf})
}
func (s *Server) portalSession(w http.ResponseWriter, r *http.Request) {
	user, _, csrf, err := s.Store.PortalIdentity(portalCookie(r), "browser")
	if err != nil {
		if errors.Is(err, store.ErrUnauthorized) {
			reply(w, 200, map[string]bool{"authenticated": false})
		} else {
			w.Header().Set("Retry-After", "5")
			fail(w, 503, "账户服务暂时不可用，请稍后重试")
		}
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
	if r.Method == http.MethodGet {
		action = "get"
	}
	if strings.HasSuffix(r.URL.Path, "/rotate") {
		action = "rotate"
	}
	if strings.HasSuffix(r.URL.Path, "/revoke") {
		action = "revoke"
	}
	var link string
	var err error
	if id := r.PathValue("id"); id != "" {
		link, err = s.Store.PortalSubscription(user, id, action)
	} else {
		link, err = s.Store.PortalAllSubscriptions(user, action)
	}
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

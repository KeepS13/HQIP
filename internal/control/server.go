package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/payment"
	"github.com/KeepS13/HQIP/internal/provision"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

type Installer interface {
	Install(context.Context, provision.Input, provision.LogFunc, provision.PinFunc) (provision.Result, error)
	Verify(context.Context, provision.Input, provision.LogFunc) (provision.Result, error)
}
type LoginLimiter interface {
	Allow(context.Context, string, int, time.Duration) (bool, error)
}
type Options struct {
	PaymentClient                                   *payment.Client
	LoginLimiter                                    LoginLimiter
	TurnstileClient                                 *http.Client
	PublicURL, AdminOrigin, UserOrigin, ArtifactDir string
	Installer                                       Installer
}
type Server struct {
	Store           *store.Store
	opts            Options
	runner          Installer
	mu              sync.Mutex
	attempts        map[string][]time.Time
	authSlots       chan struct{}
	wake            chan struct{}
	initErr         error
	operations      operationRuntime
	adminLoginLocal adminLoginProtection
}

func New(s *store.Store, opts Options) *Server {
	if opts.AdminOrigin == "" {
		opts.AdminOrigin = opts.PublicURL
	}
	if opts.UserOrigin == "" {
		opts.UserOrigin = opts.PublicURL
	}
	runner := opts.Installer
	controllerID, initErr := s.ControllerID()
	if runner == nil {
		runner = provision.New(provision.Options{ControlURL: opts.PublicURL, ArtifactDir: opts.ArtifactDir, ControllerID: controllerID})
	}
	return &Server{Store: s, opts: opts, runner: runner, attempts: map[string][]time.Time{}, authSlots: make(chan struct{}, 2), wake: make(chan struct{}, 1), initErr: initErr}
}

func (s *Server) Handler() http.Handler {
	return s.AdminHandler()
}

func (s *Server) AdminHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"ok": true}) })
	m.HandleFunc("GET /api/v1/session", s.session)
	m.HandleFunc("POST /api/v1/login", s.login)
	m.Handle("POST /api/v1/logout", s.auth(http.HandlerFunc(s.logout), true))
	m.Handle("POST /api/v1/password", s.auth(http.HandlerFunc(s.password), true))
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/overview":          s.overview,
		"GET /api/v1/traffic/analytics": s.trafficAnalytics,
		"GET /api/v1/nodes":             s.listNodes, "POST /api/v1/nodes": s.createNode,
		"GET /api/v1/nodes/{id}": s.nodeDetail, "PATCH /api/v1/nodes/{id}": s.updateNode,
		"POST /api/v1/nodes/{id}/retry": s.retryNode, "POST /api/v1/nodes/{id}/verify": s.verifyNode,
		"POST /api/v1/nodes/{id}/external-verification": s.externalVerification,
		"POST /api/v1/nodes/{id}/verification-config":   s.verificationConfig,
		"GET /api/v1/nodes/{id}/events":                 s.events,
		"GET /api/v1/devices":                           s.listDevices, "POST /api/v1/devices": s.createDevice,
		"GET /api/v1/devices/{id}": s.deviceDetail, "PATCH /api/v1/devices/{id}": s.updateDevice,
		"POST /api/v1/devices/{id}/rotate":      s.rotateDevice,
		"PATCH /api/v1/devices/{id}/assignment": s.assignment,
		"GET /api/v1/rules":                     s.getRules, "PUT /api/v1/rules": s.putRules, "GET /api/v1/rules/export": s.exportRules,
	} {
		m.Handle(pattern, s.auth(handler, false))
	}
	s.registerServerRoutes(m)
	s.registerServerOperations(m)
	s.registerHostResourceRoutes(m)
	s.registerCloudflareRoutes(m)
	s.registerPortalAdmin(m)
	s.registerNotificationRoutes(m)
	s.registerNetworkQualityRoutes(m)
	s.registerOperationLogs(m)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	return s.operationRequests(m, s.headers(m, s.opts.AdminOrigin), true)
}

// PublicHandler deliberately has no administrator route or SPA fallback.
// It remains safe even if the reverse proxy forwards an unexpected URL.
func (s *Server) PublicHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"ok": true}) })
	m.HandleFunc("GET /api/v1/agents/config", s.agentConfig)
	m.HandleFunc("POST /api/v1/agents/report", s.agentReport)
	m.HandleFunc("GET /sub/{token}", s.subscription)
	// The retired device-account API cannot create another authorization path.
	// Current clients use the account portal; subscription URLs remain unchanged.
	m.HandleFunc("/api/v1/client/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusGone, "请升级 HQIP 3.0，使用邮箱账户在浏览器登录")
	})
	s.registerPortalPublic(m)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	return s.operationRequests(m, s.headers(m, s.opts.UserOrigin), false)
}

func (s *Server) headers(m http.Handler, origin string) http.Handler {
	// Bound concurrent work before parsing bodies or waiting for a database
	// connection. Overload is explicit and retryable, not an unbounded queue.
	slots := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		browserAPI := strings.HasPrefix(r.URL.Path, "/api/") && (origin == s.opts.AdminOrigin || strings.HasPrefix(r.URL.Path, "/api/v1/user/"))
		if browserAPI {
			w.Header().Add("Vary", "Origin")
			supplied := r.Header.Get("Origin")
			if supplied != "" && supplied != origin {
				fail(w, 403, "请求来源无效")
				return
			}
			if supplied == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Expose-Headers", "ETag, Content-Disposition")
			}
			if r.Method == http.MethodOptions {
				if supplied != origin {
					fail(w, 403, "请求来源无效")
					return
				}
				method := r.Header.Get("Access-Control-Request-Method")
				if !strings.Contains(" GET POST PUT PATCH DELETE HEAD ", " "+method+" ") || method == "" {
					fail(w, 405, "不支持的请求方式")
					return
				}
				for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
					switch strings.ToLower(strings.TrimSpace(header)) {
					case "", "content-type", "x-csrf-token", "if-none-match":
					default:
						fail(w, 403, "请求头不允许")
						return
					}
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token, If-None-Match")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(origin, "https://") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/sub/") || strings.HasPrefix(r.URL.Path, "/usub/") || r.URL.Path == "/authorize" {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.URL.Path != "/healthz" {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				w.Header().Set("Retry-After", "1")
				fail(w, http.StatusServiceUnavailable, "服务繁忙，请稍后重试")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.Store.DB.PingContext(ctx); err != nil {
		fail(w, 503, "服务暂不可用")
		return
	}
	if s.opts.LoginLimiter != nil {
		probe, ok := s.opts.LoginLimiter.(interface{ Ping(context.Context) error })
		if !ok || probe.Ping(ctx) != nil {
			fail(w, 503, "服务暂不可用")
			return
		}
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, 415, "请使用 JSON 请求格式")
		return false
	}
	limit := int64(4 << 20)
	if r.URL.Path == "/api/v1/rules" {
		limit = 9 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, 400, "请求数据格式无效或超过大小限制")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		fail(w, 400, "请求中只能包含一个 JSON 对象")
		return false
	}
	return true
}
func result(w http.ResponseWriter, v any, err error) {
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, 404, "记录不存在")
		} else {
			fail(w, 500, "操作失败，请检查服务日志或备份状态")
		}
		return
	}
	reply(w, 200, v)
}
func cookieToken(r *http.Request) string {
	c, err := r.Cookie("hqip_session")
	if err != nil {
		return ""
	}
	return c.Value
}
func (s *Server) setCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "hqip_session", Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.opts.AdminOrigin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	p, e2 := url.Parse(s.opts.AdminOrigin)
	return e == nil && e2 == nil && u.Scheme == p.Scheme && u.Host == p.Host
}

func (s *Server) auth(next http.Handler, allowInitial bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := cookieToken(r)
		if token == "" {
			fail(w, 401, "请先登录")
			return
		}
		csrf, err := s.Store.Session(token)
		if err != nil {
			fail(w, 401, "登录已过期，请重新登录")
			return
		}
		username, _, must, err := s.Store.Admin()
		if err != nil {
			fail(w, 503, "管理员尚未初始化")
			return
		}
		setOperationActor(r, "admin", "1", username, "session")
		if r.Method != "GET" && r.Method != "HEAD" {
			if !s.sameOrigin(r) || !secure.Equal(csrf, r.Header.Get("X-CSRF-Token")) {
				fail(w, 403, "安全校验失败，请刷新页面后重试")
				return
			}
		}
		if must && !allowInitial {
			fail(w, 403, "首次登录请先修改初始密码")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	csrf, err := s.Store.Session(cookieToken(r))
	if err != nil {
		reply(w, 200, map[string]bool{"authenticated": false})
		return
	}
	username, _, must, err := s.Store.Admin()
	if err != nil {
		reply(w, 200, map[string]bool{"authenticated": false})
		return
	}
	reply(w, 200, map[string]any{"authenticated": true, "username": username, "csrfToken": csrf, "mustChangePassword": must})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		if forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]; net.ParseIP(strings.TrimSpace(forwarded)) != nil {
			return strings.TrimSpace(forwarded)
		}
	}
	return host
}
func (s *Server) allowLogin(w http.ResponseWriter, r *http.Request, ip string) bool {
	if s.opts.LoginLimiter != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		allowed, err := s.opts.LoginLimiter.Allow(ctx, ip, 10, 15*time.Minute)
		if err != nil {
			w.Header().Set("Retry-After", "5")
			fail(w, http.StatusServiceUnavailable, "安全校验服务暂不可用，请稍后重试")
			return false
		}
		if !allowed {
			w.Header().Set("Retry-After", "900")
			fail(w, http.StatusTooManyRequests, "尝试过多，请稍后重试")
		}
		return allowed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Store.Now()
	cutoff := now.Add(-15 * time.Minute)
	// Purge idle keys so arbitrary client IPs cannot grow this map indefinitely.
	for key, values := range s.attempts {
		kept := values[:0]
		for _, at := range values {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(s.attempts, key)
		} else {
			s.attempts[key] = kept
		}
	}
	if len(s.attempts[ip]) >= 10 {
		w.Header().Set("Retry-After", "900")
		fail(w, http.StatusTooManyRequests, "尝试过多，请稍后重试")
		return false
	}
	if len(s.attempts) > 10000 {
		w.Header().Set("Retry-After", "900")
		fail(w, http.StatusTooManyRequests, "请求繁忙，请稍后重试")
		return false
	}
	s.attempts[ip] = append(s.attempts[ip], now)
	return true
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		fail(w, 403, "不允许跨站登录")
		return
	}
	guard, allowed := s.beginAdminLogin(w, r)
	if !allowed {
		return
	}
	// Bound login parsing before the general JSON decoder's larger API budget.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Username) > 80 || len(body.Password) > 1024 {
		fail(w, 401, "账号或密码错误")
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		w.Header().Set("Retry-After", "2")
		fail(w, 429, "登录请求繁忙，请稍后重试")
		return
	}
	username, hash, must, err := s.Store.Admin()
	if err != nil {
		fail(w, 503, "管理员尚未初始化")
		return
	}
	valid := secure.CheckPassword(hash, body.Password)
	if !secure.Equal(username, body.Username) || !valid {
		if !recordAdminLoginFailure(w, r, guard) {
			return
		}
		s.Store.Audit("login.failed", clientIP(r))
		fail(w, 401, "账号或密码错误")
		return
	}
	token, csrf, err := s.Store.CreateSession(hash)
	if errors.Is(err, store.ErrUnauthorized) {
		fail(w, 401, "密码已变更，请重新登录")
		return
	}
	if err != nil {
		fail(w, 500, "无法创建登录会话")
		return
	}
	if !recordAdminLoginSuccess(w, r, guard) {
		s.Store.DeleteSession(token)
		return
	}
	setOperationActor(r, "admin", "1", username, "password")
	s.setCookie(w, token, 43200)
	s.Store.Audit("login.success", username)
	reply(w, 200, map[string]any{"authenticated": true, "username": username, "csrfToken": csrf, "mustChangePassword": must})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Store.DeleteSession(cookieToken(r))
	s.setCookie(w, "", -1)
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) password(w http.ResponseWriter, r *http.Request) {
	if !s.beginPasswordWork(w, r, "admin") {
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
	if len([]rune(b.New)) < 12 || len(b.New) > 1024 {
		fail(w, 400, "新密码长度应为 12–1024 个字符")
		return
	}
	if b.New == b.Current {
		fail(w, 400, "新密码不能与当前密码相同")
		return
	}
	_, hash, _, err := s.Store.Admin()
	if err != nil || !secure.CheckPassword(hash, b.Current) {
		fail(w, 400, "当前密码不正确")
		return
	}
	token, _, err := s.Store.ChangePasswordAndCreateSession(hash, b.New)
	if errors.Is(err, store.ErrUnauthorized) {
		fail(w, 401, "密码已变更，请重新登录")
		return
	}
	if err != nil {
		fail(w, 500, "修改密码失败")
		return
	}
	s.setCookie(w, token, 43200)
	s.Store.Audit("password.changed", "admin")
	reply(w, 200, map[string]bool{"ok": true})
}

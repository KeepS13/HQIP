package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type portalTransport func(*http.Request) (*http.Response, error)

func (f portalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func portalRequest(h http.Handler, method, path string, body any, cookie, csrf, origin string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.RemoteAddr = "198.51.100.88:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("Cookie", cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func portalLoginFixture(t *testing.T, s *Server, email string) (model.PortalUser, string, string) {
	t.Helper()
	u, e := s.Store.RegisterPortal(email, "safe-user-password-123")
	if e != nil {
		t.Fatal(e)
	}
	tokens, csrf, e := s.Store.PortalLogin(email, "safe-user-password-123")
	if e != nil {
		t.Fatal(e)
	}
	return u, "hqip_user=" + tokens.AccessToken, csrf
}

func TestPortalTurnstileFailClosedAndChecksHostnameActionTime(t *testing.T) {
	s, _ := newServer(t)
	enc, _ := s.Store.Vault.Encrypt("PRIVATE-TURNSTILE-SECRET")
	_ = s.Store.SetPortalSetting("turnstile_secret", enc)
	_ = s.Store.SetPortalSetting("turnstile_site_key", "public-site-key")
	base, _ := url.Parse(s.opts.PublicURL)
	for _, tc := range []struct {
		name         string
		success      bool
		host, action string
		stamp        time.Time
		failure      bool
		want         bool
	}{{"valid", true, base.Hostname(), "login", s.Store.Now(), false, true}, {"wrong-host", true, "evil.example", "login", s.Store.Now(), false, false}, {"wrong-action", true, base.Hostname(), "register", s.Store.Now(), false, false}, {"expired", true, base.Hostname(), "login", s.Store.Now().Add(-6 * time.Minute), false, false}, {"failure", false, base.Hostname(), "login", s.Store.Now(), false, false}, {"network-error", true, base.Hostname(), "login", s.Store.Now(), true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s.opts.TurnstileClient = &http.Client{Transport: portalTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://challenges.cloudflare.com/turnstile/v0/siteverify" {
					t.Fatal("verification URL changed")
				}
				_ = r.ParseForm()
				if r.Form.Get("secret") != "PRIVATE-TURNSTILE-SECRET" {
					t.Fatal("secret not provided only to verifier")
				}
				if tc.failure {
					return nil, errors.New("offline")
				}
				b, _ := json.Marshal(map[string]any{"success": tc.success, "hostname": tc.host, "action": tc.action, "challenge_ts": tc.stamp})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b))}, nil
			})}
			if got := s.verifyTurnstile(context.Background(), "captcha-token", "198.51.100.1", "login"); got != tc.want {
				t.Fatalf("verification=%v want %v", got, tc.want)
			}
		})
	}
	_ = s.Store.SetPortalSetting("turnstile_secret", "")
	if s.verifyTurnstile(context.Background(), "token", "198.51.100.1", "login") {
		t.Fatal("missing Turnstile config failed open")
	}
}
func TestPortalRegistrationRequiresTurnstileAndUsesSeparateSecureSession(t *testing.T) {
	s, _ := newServer(t)
	h := s.PublicHandler()
	_ = s.Store.SetPortalSetting("portal_registration", "true")
	body := map[string]string{"email": "new@example.com", "password": "long-user-password-123", "turnstileToken": "test-token"}
	if w := portalRequest(h, "POST", "/api/v1/user/register", body, "", "", s.opts.PublicURL); w.Code != 403 {
		t.Fatalf("missing CAPTCHA configuration accepted %d", w.Code)
	}
	enc, _ := s.Store.Vault.Encrypt("PRIVATE-TURNSTILE-SECRET")
	_ = s.Store.SetPortalSetting("turnstile_secret", enc)
	_ = s.Store.SetPortalSetting("turnstile_site_key", "public-key")
	base, _ := url.Parse(s.opts.PublicURL)
	s.opts.TurnstileClient = &http.Client{Transport: portalTransport(func(r *http.Request) (*http.Response, error) {
		raw, _ := json.Marshal(map[string]any{"success": true, "hostname": base.Hostname(), "action": "register", "challenge_ts": s.Store.Now()})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})}
	w := portalRequest(h, "POST", "/api/v1/user/register", body, "", "", s.opts.PublicURL)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "hqip_user" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe user cookie %+v", cookies)
	}
	for _, secret := range []string{"PRIVATE-TURNSTILE-SECRET", "long-user-password-123", "accessToken", "refreshToken"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("browser login response leaked token or secret")
		}
	}
}
func TestPortalCSRFAndCrossRoleIsolation(t *testing.T) {
	s, h := newServer(t)
	adminCookie, adminCSRF := loginAdmin(t, s, h)
	user, cookie, csrf := portalLoginFixture(t, s, "user@example.com")
	other, _, _ := portalLoginFixture(t, s, "other@example.com")
	public := s.PublicHandler()
	sub := subscriptionFixture(t, s, user, nil, "", 1)
	foreignSub := subscriptionFixture(t, s, other, sub.NodeIDs, sub.DefaultNodeID, 1)
	importPath := "/api/v1/user/subscriptions/" + sub.ID + "/import-link"
	for _, test := range []struct{ origin, csrf string }{{"", csrf}, {"https://evil.invalid", csrf}, {s.opts.PublicURL, "wrong"}} {
		w := portalRequest(public, "POST", importPath, map[string]any{}, cookie, test.csrf, test.origin)
		if w.Code != 403 {
			t.Fatal("missing CSRF or origin accepted", w.Code)
		}
	}
	if w := portalRequest(public, "POST", importPath, map[string]any{}, cookie, csrf, s.opts.PublicURL); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request(s.AdminHandler(), "GET", "/api/v1/users", nil, cookie, csrf); w.Code != 401 {
		t.Fatal("user cookie authorized administrator")
	}
	if w := request(public, "GET", "/api/v1/user/profile", nil, adminCookie, adminCSRF); w.Code != 401 {
		t.Fatal("admin cookie authorized user realm")
	}
	if w := portalRequest(public, "POST", importPath, map[string]string{"userId": other.ID}, cookie, csrf, s.opts.PublicURL); w.Code != 200 {
		t.Fatal("non-JSON-body handler should be session scoped")
	}
	own, _ := s.Store.PortalSubscription(user.ID, sub.ID, "get")
	foreign, _ := s.Store.PortalSubscription(other.ID, foreignSub.ID, "get")
	if own == "" || foreign != "" {
		t.Fatal("request body changed user identity")
	}
	w := request(public, "GET", "/api/v1/user/profile", nil, cookie, "")
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "sshHost") || strings.Contains(w.Body.String(), other.Email) {
		t.Fatal("profile leaked secrets/other user")
	}
	for _, path := range []string{"/api/v1/subscription-templates", "/api/v1/users", "/api/v1/settings/turnstile", "/admin"} {
		if w := request(public, "GET", path, nil, adminCookie, adminCSRF); w.Code != 404 {
			t.Fatal("public exposed admin route", path)
		}
	}
}
func TestPortalNativeAuthorizationAndScopedDrain(t *testing.T) {
	s, _ := newServer(t)
	u, cookie, csrf := portalLoginFixture(t, s, "desktop@example.com")
	public := s.PublicHandler()
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "http://127.0.0.1:45231/callback"
	w := bearerRequest(public, "POST", "/api/v3/client/authorize", map[string]string{"codeChallenge": challenge, "redirectUri": redirect, "state": strings.Repeat("s", 32), "clientName": "test client", "installationId": "test-installation"}, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var begin struct {
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &begin)
	w = portalRequest(public, "POST", "/api/v1/user/authorize/"+begin.RequestID, map[string]any{}, cookie, csrf, s.opts.PublicURL)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var approval struct {
		RedirectURL string `json:"redirectUrl"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &approval)
	target, _ := url.Parse(approval.RedirectURL)
	w = bearerRequest(public, "POST", "/api/v3/client/token", map[string]string{"code": target.Query().Get("code"), "codeVerifier": verifier, "redirectUri": redirect, "installationId": "test-installation"}, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var tokens model.PortalTokens
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	w = bearerRequest(public, "GET", "/api/v3/client/profile", nil, tokens.AccessToken)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var profile struct {
		Enabled           bool                    `json:"enabled"`
		Subscription      *model.UserSubscription `json:"subscription"`
		SelectionRevision int64                   `json:"selectionRevision"`
		DeviceID          string                  `json:"deviceId"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &profile)
	if !profile.Enabled || profile.Subscription != nil || profile.DeviceID != u.ID {
		t.Fatal("unassigned user cannot log in or profile mis-scoped")
	}
	if w = bearerRequest(public, "POST", "/api/v3/client/connections/drain", map[string]any{"subscriptionId": "", "expectedRevision": profile.SelectionRevision}, tokens.AccessToken); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = bearerRequest(public, "GET", "/api/v1/client/profile", nil, tokens.AccessToken); w.Code != 401 {
		t.Fatal("v2 token worked on legacy client surface")
	}
}
func TestPortalSettingsNeverEchoSecretAndActivationIsExplicit(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	secret := "PRIVATE-TURNSTILE-SECRET"
	w := request(h, "PUT", "/api/v1/settings/turnstile", map[string]string{"siteKey": "site-key", "secretKey": secret}, cookie, csrf)
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) {
		t.Fatal("secret settings failed/leaked", w.Body.String())
	}
	w = request(h, "PUT", "/api/v1/settings/turnstile", map[string]any{"siteKey": "new-site-key", "expectedRevision": 1}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	enc := s.Store.PortalSetting("turnstile_settings")
	plain, e := s.Store.TurnstileSecret()
	if e != nil || plain != secret || strings.Contains(enc, secret) {
		t.Fatal("secret not encrypted or omission erased it")
	}
	if w = request(h, "POST", "/api/v1/settings/portal/activate", map[string]bool{"confirm": false}, cookie, csrf); w.Code != 400 || s.Store.PortalEnabled() {
		t.Fatal("unconfirmed activation occurred")
	}
	w = httptest.NewRecorder()
	portalResult(w, nil, errors.New("SQL table portal_users SECRET password"))
	if strings.Contains(w.Body.String(), "SQL") || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal("internal error escaped")
	}
}

func TestPortalActivationRejectsStaleNodeCapability(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	n, e := s.Store.CreateNode(model.Node{Name: "node", SSHHost: "203.0.113.1", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.1", Domain: "node.example.com", Port: 443}, "")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Store.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]',core_healthy=1,last_seen=?,applied_revision=desired_revision WHERE id=?", s.Store.Now().Add(-31*time.Second).Unix(), n.ID)
	if e != nil {
		t.Fatal(e)
	}
	w := request(h, "POST", "/api/v1/settings/portal/activate", map[string]bool{"confirm": true}, cookie, csrf)
	if w.Code != 409 || s.Store.PortalEnabled() {
		t.Fatal("historical capability activated offline node")
	}
	_, _ = s.Store.DB.Exec("UPDATE nodes SET last_seen=? WHERE id=?", s.Store.Now().Unix(), n.ID)
	w = request(h, "POST", "/api/v1/settings/portal/activate", map[string]bool{"confirm": true}, cookie, csrf)
	if w.Code != 200 || !s.Store.PortalEnabled() {
		t.Fatal("fresh healthy capable node could not activate", w.Body.String())
	}
}

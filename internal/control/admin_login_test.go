package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/ratelimit"
	"github.com/alicebob/miniredis/v2"
)

func adminLoginRequest(h http.Handler, ip, password, forwarded string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	r := httptest.NewRequest("POST", "/api/v1/login", bytes.NewReader(body))
	r.RemoteAddr = ip
	r.Header.Set("Content-Type", "application/json")
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAdminLoginTemporaryBanLeavesExistingSessionUsable(t *testing.T) {
	s, h := newServer(t)
	now := time.Unix(1800000000, 0)
	s.Store.Now = func() time.Time { return now }
	good := "initial-password-123"
	w := adminLoginRequest(h, "198.51.100.1:1", good, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0].String()
	for i := 0; i < 5; i++ {
		if w := adminLoginRequest(h, "198.51.100.1:1", "incorrect", ""); w.Code != 401 {
			t.Fatal(i, w.Code)
		}
	}
	if w := adminLoginRequest(h, "198.51.100.1:1", good, ""); w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatal(w.Code, w.Header())
	}
	if w := request(h, "GET", "/api/v1/session", nil, cookie, ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"authenticated":true`)) {
		t.Fatal("ban revoked existing session", w.Code, w.Body.String())
	}
	now = now.Add(time.Minute)
	if w := adminLoginRequest(h, "198.51.100.1:1", good, ""); w.Code != 200 {
		t.Fatal("temporary ban did not expire", w.Code, w.Body.String())
	}
}

func TestAdminLoginDoesNotTrustSpoofedForwardingHeaders(t *testing.T) {
	_, h := newServer(t)
	for i := 0; i < 5; i++ {
		if w := adminLoginRequest(h, "[2001:db8:1:2::1]:1234", "incorrect", "192.0.2.1"); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := adminLoginRequest(h, "[2001:db8:1:2::99]:1234", "initial-password-123", "192.0.2.99"); w.Code != 429 {
		t.Fatal("IPv6 address rotation or spoofed XFF bypassed ban", w.Code)
	}
	if w := adminLoginRequest(h, "[2001:db8:1:3::1]:1234", "initial-password-123", "192.0.2.1"); w.Code != 200 {
		t.Fatal("unrelated subnet blocked", w.Code)
	}
}

func TestAdminLoginSharedRedisBanAndFailureHandling(t *testing.T) {
	db := miniredis.RunT(t)
	db.SetTime(time.Unix(1800000000, 0))
	r, err := ratelimit.Open(context.Background(), "redis://"+db.Addr(), "login-test")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s, h := newServer(t)
	s.opts.LoginLimiter = r
	for i := 0; i < 5; i++ {
		if w := adminLoginRequest(h, "198.51.100.1:1", "incorrect", ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := adminLoginRequest(h, "198.51.100.1:1", "initial-password-123", ""); w.Code != 429 {
		t.Fatal(w.Code)
	}
	db.Close()
	if w := adminLoginRequest(h, "198.51.100.2:1", "initial-password-123", ""); w.Code != 503 || len(w.Result().Cookies()) > 0 {
		t.Fatal("Redis outage issued session", w.Code)
	}
	if s.adminLoginLocal != nil {
		t.Fatal("Redis failure created a local fallback")
	}
}

type failureAfterPasswordProtection struct{ stage string }

func (f failureAfterPasswordProtection) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return true, nil
}
func (f failureAfterPasswordProtection) AdminAdmit(context.Context, string) (ratelimit.AdminDecision, error) {
	return ratelimit.AdminDecision{Allowed: true}, nil
}
func (f failureAfterPasswordProtection) AdminFailure(context.Context, string) (ratelimit.AdminDecision, error) {
	return ratelimit.AdminDecision{}, errors.New("unavailable")
}
func (f failureAfterPasswordProtection) AdminSuccess(context.Context, string) error {
	return errors.New("unavailable")
}

func TestAdminLoginCannotIssueSessionWhenProtectionUpdateFails(t *testing.T) {
	s, h := newServer(t)
	s.opts.LoginLimiter = failureAfterPasswordProtection{}
	for _, password := range []string{"incorrect", "initial-password-123"} {
		w := adminLoginRequest(h, "198.51.100.1:1", password, "")
		if w.Code != 503 || len(w.Result().Cookies()) != 0 {
			t.Fatal(w.Code, w.Header())
		}
	}
	var count int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatal("undelivered session retained", count, err)
	}
}

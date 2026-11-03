package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPSAdministratorCookieAndOrigin(t *testing.T) {
	s, _ := newServer(t)
	s.opts.AdminOrigin = "https://hqip.5202345.xyz"
	for _, origin := range []string{s.opts.AdminOrigin, "https://attacker.example", "http://hqip.5202345.xyz"} {
		r := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"initial-password-123"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.AdminHandler().ServeHTTP(w, r)
		if origin != s.opts.AdminOrigin {
			if w.Code != 403 {
				t.Fatalf("foreign origin returned %d", w.Code)
			}
			continue
		}
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Fatal("unsafe administrator cookie")
		}
		if w.Header().Get("Strict-Transport-Security") == "" {
			t.Fatal("missing HSTS")
		}
	}
}

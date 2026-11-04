package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSeparateOriginsCORSAndSessionIsolation(t *testing.T) {
	s, _ := newServer(t)
	s.opts.AdminOrigin = "https://cms-hqip.5202345.xyz"
	s.opts.UserOrigin = "https://hqip.5202345.xyz"
	s.opts.PublicURL = "https://api-hqip.5202345.xyz"
	for _, test := range []struct {
		h            http.Handler
		path, origin string
		code         int
	}{
		{s.AdminHandler(), "/api/v1/session", s.opts.AdminOrigin, 200},
		{s.AdminHandler(), "/api/v1/session", s.opts.UserOrigin, 403},
		{s.PublicHandler(), "/api/v1/user/config", s.opts.UserOrigin, 200},
		{s.PublicHandler(), "/api/v1/user/config", s.opts.AdminOrigin, 403},
		{s.PublicHandler(), "/api/v1/user/config", "https://evil.5202345.xyz", 403},
	} {
		r := httptest.NewRequest("GET", test.path, nil)
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		test.h.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Fatalf("%s %s: %d", test.path, test.origin, w.Code)
		}
		if test.code == 200 && (w.Header().Get("Access-Control-Allow-Origin") != test.origin || w.Header().Get("Access-Control-Allow-Credentials") != "true") {
			t.Fatal("credentialed CORS missing")
		}
	}
	r := httptest.NewRequest("OPTIONS", "/api/v1/alerts/test", nil)
	r.Header.Set("Origin", s.opts.AdminOrigin)
	r.Header.Set("Access-Control-Request-Method", "PATCH")
	r.Header.Set("Access-Control-Request-Headers", "content-type,x-csrf-token")
	w := httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"initial-password-123"}`))
	r.Header.Set("Origin", s.opts.AdminOrigin)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	c := w.Result().Cookies()[0]
	if c.Domain != "" || !c.Secure || !c.HttpOnly {
		t.Fatal("cookie domain isolation lost")
	}
	for _, path := range []string{"/api/v1/alerts", "/api/v1/alerts/summary", "/api/v1/alerts/test"} {
		if w = request(s.AdminHandler(), "GET", path, nil, "", ""); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
		if w = request(s.PublicHandler(), "GET", path, nil, "", ""); w.Code != 404 {
			t.Fatal("public admin leak", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/settings/feishu", "/api/v1/settings/feishu/test", "/api/v1/notifications"} {
		if w = request(s.AdminHandler(), "GET", path, nil, "", ""); w.Code != 404 {
			t.Fatal("retired sender API", path, w.Code)
		}
	}
}

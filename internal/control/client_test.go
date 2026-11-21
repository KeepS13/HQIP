package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bearerRequest(h http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	if body == nil {
		raw = nil
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPublicMuxCannotReachAdministratorOrSPA(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	public := s.PublicHandler()
	for _, path := range []string{"/admin", "/index.html", "/api/v1/session", "/api/v1/login", "/api/v1/nodes", "/api/v1/devices", "/api/v1/rules", "/api/v1/password", "/api/v1/nodes/node/verification-config"} {
		for _, method := range []string{"GET", "POST"} {
			w := request(public, method, path, map[string]string{}, cookie, csrf)
			if w.Code != 404 {
				t.Fatalf("public %s %s responded %d", method, path, w.Code)
			}
		}
	}
	if w := request(s.AdminHandler(), "GET", "/api/v1/client/profile", nil, "", ""); w.Code != 404 {
		t.Fatal("device routes were registered on admin surface")
	}
}

func TestLocalAdministratorOriginAndCookieDoNotFollowPublicURL(t *testing.T) {
	s, _ := newServer(t)
	s.opts.AdminOrigin = "http://127.0.0.1:8080"
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/v1/login", strings.NewReader(`{"username":"admin","password":"initial-password-123"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.opts.AdminOrigin)
	w := httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS public URL made localhost administrator cookie unusable")
	}
}

func TestRetiredDeviceAccountCannotBeCreated(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	w := request(s.AdminHandler(), "POST", "/api/v1/devices/old/client-account", map[string]string{"username": "old-user", "password": "old-device-password"}, cookie, csrf)
	if w.Code != http.StatusNotFound {
		t.Fatal("retired account route still registered", w.Code)
	}
}

package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
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
	for _, path := range []string{"/", "/index.html", "/api/v1/session", "/api/v1/login", "/api/v1/nodes", "/api/v1/devices", "/api/v1/rules", "/api/v1/password", "/api/v1/nodes/node/verification-config"} {
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

func TestDeviceProfileETagSessionIsolationAndRulesHash(t *testing.T) {
	s, _ := newServer(t)
	n, err := s.Store.CreateNode(model.Node{Name: "candidate", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "ssh-secret")
	if err != nil {
		t.Fatal(err)
	}
	d, err := createAssignedDevice(t, s, "computer")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetClientAccount(d.ID, "computer-user", "a-strong-device-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.Rules(); err != nil {
		t.Fatal(err)
	}
	public := s.PublicHandler()
	w := bearerRequest(public, "POST", "/api/v1/client/login", map[string]string{"username": "computer-user", "password": "a-strong-device-password", "installationId": "install-0001", "clientName": "windows"}, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var tokens model.ClientTokens
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	w = bearerRequest(public, "GET", "/api/v1/client/profile", nil, tokens.AccessToken)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var p model.ClientProfile
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.DeviceID != d.ID || len(p.Nodes) != 1 || p.Nodes[0].ID != n.ID {
		t.Fatalf("wrong scoped profile: %+v", p)
	}
	if strings.Contains(w.Body.String(), "sshHost") || strings.Contains(w.Body.String(), "ssh-secret") {
		t.Fatal("profile leaked management details")
	}
	r := httptest.NewRequest("GET", "/api/v1/client/profile", nil)
	r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	public.ServeHTTP(w, r)
	if w.Code != 304 {
		t.Fatal("unchanged profile did not return 304")
	}
	w = bearerRequest(public, "GET", "/api/v1/client/rules/"+p.Rules.Hash, nil, tokens.AccessToken)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	raw, _ := s.Store.RuleBytes(p.Rules.Hash)
	if !bytes.Equal(raw, w.Body.Bytes()) {
		t.Fatal("downloaded rules not byte identical to hash input")
	}
	w = bearerRequest(s.AdminHandler(), "GET", "/api/v1/nodes", nil, tokens.AccessToken)
	if w.Code != 401 {
		t.Fatal("client bearer token entered administrator API")
	}
	var status = map[string]any{"deviceId": "another-device", "runState": "connected"}
	if w = bearerRequest(public, "POST", "/api/v1/client/status", status, tokens.AccessToken); w.Code != 400 {
		t.Fatal("client selected another device in request body")
	}
}

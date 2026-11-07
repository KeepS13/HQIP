package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/provision"
	"github.com/KeepS13/HQIP/internal/store"
	"gopkg.in/yaml.v3"
)

func newServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s, err := controlTestStore(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.InitAdmin("admin", "initial-password-123"); err != nil {
		t.Fatal(err)
	}
	server := New(s, Options{PublicURL: s.BaseURL})
	// Historic behavior tests exercise both isolated surfaces through a test-only
	// dispatcher. Production never exposes this combined handler.
	admin, public := server.AdminHandler(), server.PublicHandler()
	return server, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sub/") || strings.HasPrefix(r.URL.Path, "/api/v1/agents/") || strings.HasPrefix(r.URL.Path, "/api/v1/client/") {
			public.ServeHTTP(w, r)
		} else {
			admin.ServeHTTP(w, r)
		}
	})
}

func createAssignedDevice(t *testing.T, s *Server, name string) (model.Device, error) {
	t.Helper()
	d, err := s.Store.CreateDevice(name)
	if err != nil {
		return d, err
	}
	nodes, err := s.Store.Nodes()
	if err != nil {
		return d, err
	}
	ids := []string{}
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	defaultID := ""
	if len(ids) > 0 {
		defaultID = ids[0]
	}
	a, err := s.Store.Assignment(d.ID)
	if err != nil {
		return d, err
	}
	_, err = s.Store.Assign(d.ID, ids, defaultID, a.Revision)
	return d, err
}
func request(h http.Handler, method, path string, body any, cookie, csrf string) *httptest.ResponseRecorder {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.RemoteAddr = "198.51.100.12:1234"
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func loginAdmin(t *testing.T, s *Server, h http.Handler) (string, string) {
	t.Helper()
	if err := s.Store.ChangePassword("changed-password-123"); err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", "/api/v1/login", map[string]string{"username": "admin", "password": "changed-password-123"}, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	return w.Result().Cookies()[0].String(), result.CSRF
}

func TestLoginPasswordChangeAndCSRF(t *testing.T) {
	_, h := newServer(t)
	w := request(h, "POST", "/api/v1/login", map[string]string{"username": "admin", "password": "initial-password-123"}, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var session struct {
		CSRF string `json:"csrfToken"`
		Must bool   `json:"mustChangePassword"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	cookie := w.Result().Cookies()[0]
	if !session.Must || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("initial session must require change with secure cookies")
	}
	if w = request(h, "GET", "/api/v1/overview", nil, cookie.String(), ""); w.Code != 403 {
		t.Fatal("initial password could manage infrastructure")
	}
	body := map[string]string{"currentPassword": "initial-password-123", "newPassword": "changed-password-456"}
	if w = request(h, "POST", "/api/v1/password", body, cookie.String(), ""); w.Code != 403 {
		t.Fatal("missing CSRF was accepted")
	}
	r := httptest.NewRequest("POST", "/api/v1/password", strings.NewReader(`{"currentPassword":"initial-password-123","newPassword":"changed-password-456"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cookie", cookie.String())
	r.Header.Set("X-CSRF-Token", session.CSRF)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation was accepted")
	}
	w = request(h, "POST", "/api/v1/password", body, cookie.String(), session.CSRF)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	newCookie := w.Result().Cookies()[0]
	if w = request(h, "GET", "/api/v1/overview", nil, cookie.String(), ""); w.Code != 401 {
		t.Fatal("old session survived password change")
	}
	if w = request(h, "GET", "/api/v1/overview", nil, newCookie.String(), ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request(h, "POST", "/api/v1/devices", map[string]string{"name": "电脑"}, newCookie.String(), session.CSRF); w.Code != 403 {
		t.Fatal("old CSRF survived password change")
	}
}

func TestLoginRateLimitAndAgentIsolation(t *testing.T) {
	s, h := newServer(t)
	for i := 0; i < 10; i++ {
		w := request(h, "POST", "/api/v1/login", map[string]string{"username": "admin", "password": "incorrect"}, "", "")
		if w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := request(h, "POST", "/api/v1/login", map[string]string{"username": "admin", "password": "initial-password-123"}, "", ""); w.Code != 429 {
		t.Fatal("login rate limit not enforced")
	}
	n, err := s.Store.CreateNode(model.Node{Name: "node", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "ssh-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, token, _, _ := s.Store.NodeSecrets(n.ID)
	r := httptest.NewRequest("GET", "/api/v1/nodes", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("agent token accessed administrator API")
	}
	r = httptest.NewRequest("POST", "/api/v1/agents/report", strings.NewReader(`{"nodeId":"someone-else","revision":1}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("unknown nodeId accepted in agent body")
	}
}

func TestSuccessfulLoginResetsOnlyItsOwnFailureBudget(t *testing.T) {
	s, h := newServer(t)
	s.attempts["198.51.100.99"] = []time.Time{s.Store.Now()}
	valid := map[string]string{"username": "admin", "password": "initial-password-123"}
	invalid := map[string]string{"username": "admin", "password": "incorrect"}
	for i := 0; i < 12; i++ {
		if w := request(h, "POST", "/api/v1/login", valid, "", ""); w.Code != 200 {
			t.Fatalf("successful login %d consumed failure budget: %d", i, w.Code)
		}
	}
	for i := 0; i < 9; i++ {
		if w := request(h, "POST", "/api/v1/login", invalid, "", ""); w.Code != 401 {
			t.Fatalf("failed attempt %d: %d", i, w.Code)
		}
	}
	if w := request(h, "POST", "/api/v1/login", valid, "", ""); w.Code != 200 {
		t.Fatalf("valid login at budget boundary: %d", w.Code)
	}
	if len(s.attempts["198.51.100.12"]) != 0 || len(s.attempts["198.51.100.99"]) != 1 {
		t.Fatal("success did not reset exactly the current IP budget")
	}
	for i := 0; i < 10; i++ {
		if w := request(h, "POST", "/api/v1/login", invalid, "", ""); w.Code != 401 {
			t.Fatalf("failure budget was not reset at attempt %d: %d", i, w.Code)
		}
	}
	if w := request(h, "POST", "/api/v1/login", invalid, "", ""); w.Code != 429 {
		t.Fatal("consecutive failures were not limited")
	}
}

func TestSubscriptionSeparatesCredentialsAndRevokes(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "node", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "private-ssh-password")
	if err != nil {
		t.Fatal(err)
	}
	d, err := createAssignedDevice(t, s, "电脑")
	if err != nil {
		t.Fatal(err)
	}
	_, agent, probe, _ := s.Store.NodeSecrets(n.ID)
	path := strings.TrimPrefix(d.SubscriptionURL, s.Store.BaseURL)
	if w := request(h, "GET", path, nil, "", ""); w.Code != 503 {
		t.Fatal("unverified node was offered in subscription")
	}
	if err = s.Store.SetStatus(n.ID, "ready", "verified", false); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", path, nil, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, secret := range []string{"private-ssh-password", agent, probe, strings.TrimPrefix(path, "/sub/")} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("subscription leaked a non-device credential")
		}
	}
	var parsed map[string]any
	if err = yaml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	proxies := parsed["proxies"].([]any)
	p := proxies[0].(map[string]any)
	if p["skip-cert-verify"] != false || p["type"] != "hysteria2" {
		t.Fatal("subscription weakens TLS or wrong protocol")
	}
	own, _ := s.Store.Credential(n.ID, d.ID)
	if p["password"] != own {
		t.Fatal("subscription has wrong device credentials")
	}
	w = request(h, "POST", "/api/v1/devices/"+d.ID+"/rotate", nil, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request(h, "GET", path, nil, "", ""); w.Code != 404 {
		t.Fatal("old subscription survived rotation")
	}
	d, _ = s.Store.Device(d.ID)
	path = strings.TrimPrefix(d.SubscriptionURL, s.Store.BaseURL)
	w = request(h, "PATCH", "/api/v1/devices/"+d.ID, map[string]bool{"enabled": false}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request(h, "GET", path, nil, "", ""); w.Code != 404 {
		t.Fatal("disabled device subscription remains accessible")
	}
}

type fakeInstaller struct{ shared bool }

func (f fakeInstaller) Install(ctx context.Context, in provision.Input, log provision.LogFunc, pin provision.PinFunc) (provision.Result, error) {
	if err := pin("SHA256:trusted"); err != nil {
		return provision.Result{}, err
	}
	log("test", in.Password+" "+in.AgentToken+" "+in.ProbePassword)
	return provision.Result{SharedHost: f.shared, ACMEViaCaddy: f.shared, ControlOnNode: f.shared, Verified: !f.shared}, nil
}
func (f fakeInstaller) Verify(ctx context.Context, in provision.Input, log provision.LogFunc) (provision.Result, error) {
	return provision.Result{SharedHost: f.shared, ACMEViaCaddy: f.shared, ControlOnNode: f.shared, Verified: !f.shared}, nil
}
func TestSharedHostWorkerRequiresExternalValidationAndErasesSSHSecret(t *testing.T) {
	s, h := newServer(t)
	s.runner = fakeInstaller{shared: true}
	cookie, csrf := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "node", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "private-ssh-password")
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.runJob(context.Background(), job)
	n, _ = s.Store.Node(n.ID)
	if n.Status != "awaiting_external" || !n.SharedHost {
		t.Fatalf("local probe was falsely considered external: %+v", n)
	}
	password, agent, probe, _ := s.Store.NodeSecrets(n.ID)
	if password != "" {
		t.Fatal("successful install retained SSH password")
	}
	events, _ := s.Store.Events(n.ID, 0)
	for _, e := range events {
		for _, secret := range []string{"private-ssh-password", agent, probe} {
			if strings.Contains(e.Message, secret) {
				t.Fatal("deployment event leaked secret")
			}
		}
	}
	w := request(h, "POST", "/api/v1/nodes/"+n.ID+"/external-verification", map[string]string{"exitIp": "203.0.113.9"}, cookie, csrf)
	if w.Code != 400 {
		t.Fatal("wrong exit IP accepted")
	}
	w = request(h, "POST", "/api/v1/nodes/"+n.ID+"/external-verification", map[string]string{"exitIp": n.PublicIP}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	n, _ = s.Store.Node(n.ID)
	if n.Status != "ready" {
		t.Fatal("external verification did not mark ready")
	}
}

func TestDeviceDetailUsesPerDeviceNodeCounters(t *testing.T) {
	s, h := newServer(t)
	cookie, _ := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "node", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "ssh-password")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := createAssignedDevice(t, s, "a")
	b, _ := createAssignedDevice(t, s, "b")
	desired, _ := s.Store.Desired(n.ID)
	err = s.Store.Report(n.ID, model.AgentReport{Revision: desired.Revision, Daily: []model.UsageBucket{{DeviceID: a.ID, Start: store.DayStart(time.Now()), Upload: 7}, {DeviceID: b.ID, Start: store.DayStart(time.Now()), Upload: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/api/v1/devices/"+a.ID, nil, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Nodes []model.Node `json:"nodes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Nodes) != 1 || result.Nodes[0].MonthUpload != 7 {
		t.Fatal("device detail included other devices' usage")
	}
}

func TestVerificationConfigStaysAdminOnlyUntilReady(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "candidate", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "ssh-password")
	if err != nil {
		t.Fatal(err)
	}
	d, err := createAssignedDevice(t, s, "verification")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/nodes/" + n.ID + "/verification-config"
	if w := request(h, "POST", path, map[string]string{"deviceId": d.ID}, cookie, csrf); w.Code != 409 {
		t.Fatal("uninstalled candidate exposed verification config")
	}
	if err = s.Store.SetStatus(n.ID, "awaiting_external", "await", true); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "POST", path, map[string]string{"deviceId": d.ID}, "", ""); w.Code != 401 {
		t.Fatal("anonymous user obtained verification config")
	}
	w := request(h, "POST", path, map[string]string{"deviceId": d.ID}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response struct {
		Config string `json:"config"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	password, _ := s.Store.Credential(n.ID, d.ID)
	if !strings.Contains(response.Config, password) {
		t.Fatal("verification did not use candidate device credential")
	}
	if w = request(h, "GET", strings.TrimPrefix(d.SubscriptionURL, s.Store.BaseURL), nil, "", ""); w.Code != 503 {
		t.Fatal("verification config prematurely published node")
	}
}

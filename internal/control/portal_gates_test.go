package control

import (
	"net/http"
	"strings"
	"testing"
)

func TestPortalActivationRequiresLegacyClientUpgrade(t *testing.T) {
	s, _ := newServer(t)
	public := s.PublicHandler()
	if w := request(public, "GET", "/api/v1/client/profile", nil, "", ""); w.Code != http.StatusGone {
		t.Fatal("retired route became available before activation", w.Code)
	}
	if err := s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/client/login", "/api/v1/client/refresh", "/api/v1/client/logout", "/api/v1/client/profile", "/api/v1/client/status", "/api/v1/client/rules/old-hash", "/api/v1/client/unknown"} {
		for _, method := range []string{"GET", "POST"} {
			w := request(public, method, path, nil, "", "")
			if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "HQIP 3.0") || !strings.Contains(w.Body.String(), "浏览器登录") {
				t.Fatalf("legacy %s %s did not give upgrade guidance: %d", method, path, w.Code)
			}
		}
	}
	if w := request(s.AdminHandler(), "GET", "/api/v1/client/profile", nil, "", ""); w.Code != 404 {
		t.Fatal("legacy gate added route to admin surface")
	}
	if w := request(public, "GET", "/api/v3/client/profile", nil, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatal("activation blocked v2 client API", w.Code)
	}
}

func TestPortalEnabledHTTPRequiresCurrentRevision(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	u, _, _ := portalLoginFixture(t, s, "enabled-revision@example.com")
	u, err := s.Store.PortalUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/users/" + u.ID
	if w := request(s.AdminHandler(), "PATCH", path, map[string]any{"enabled": false}, admin, csrf); w.Code != http.StatusConflict {
		t.Fatal("enabled mutation accepted missing revision", w.Code)
	}
	if w := request(s.AdminHandler(), "PATCH", path, map[string]any{"enabled": false, "expectedRevision": u.Revision}, admin, csrf); w.Code != 200 {
		t.Fatal("current revision rejected", w.Code, w.Body.String())
	}
	if w := request(s.AdminHandler(), "PATCH", path, map[string]any{"enabled": true, "expectedRevision": u.Revision}, admin, csrf); w.Code != http.StatusConflict {
		t.Fatal("enabled mutation accepted stale revision", w.Code)
	}
}

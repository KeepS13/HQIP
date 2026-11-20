package control

import (
	"encoding/json"
	"github.com/KeepS13/HQIP/internal/model"
	"net/http"
	"testing"
)

func TestTrafficBillingAdminIsolationCASAndPrecision(t *testing.T) {
	s, _ := newServer(t)
	admin, public := s.AdminHandler(), s.PublicHandler()
	cookie, csrf := loginAdmin(t, s, admin)
	for _, handler := range []http.Handler{admin, public} {
		if result := request(handler, "GET", "/api/v1/settings/traffic-billing", nil, "", ""); result.Code == 200 {
			t.Fatal("anonymous settings exposure")
		}
	}
	if result := request(public, "GET", "/api/v1/settings/traffic-billing", nil, cookie, csrf); result.Code == 200 {
		t.Fatal("admin settings on public surface")
	}
	if result := request(public, "GET", "/api/v3/client/billing?nodeId=a&protocol=hysteria2", nil, "", ""); result.Code == 200 {
		t.Fatal("anonymous effective policy exposure")
	}
	var settings model.TrafficBillingSettings
	result := request(admin, "GET", "/api/v1/settings/traffic-billing", nil, cookie, csrf)
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &settings) != nil {
		t.Fatal(result.Code, result.Body.String())
	}
	body := map[string]any{"enabled": false, "expectedRevision": settings.Revision, "hysteria2": 150.5, "vless": 150}
	if result = request(admin, "PUT", "/api/v1/settings/traffic-billing", body, cookie, csrf); result.Code != 400 {
		t.Fatal("fractional stored integer accepted", result.Code)
	}
	body["hysteria2"] = 150
	if result = request(admin, "PUT", "/api/v1/settings/traffic-billing", body, cookie, ""); result.Code != 403 {
		t.Fatal("CSRF bypass", result.Code)
	}
	if result = request(admin, "PUT", "/api/v1/settings/traffic-billing", body, cookie, csrf); result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	if result = request(admin, "PUT", "/api/v1/settings/traffic-billing", body, cookie, csrf); result.Code != 409 {
		t.Fatal("stale version overwrite", result.Code)
	}
	updated, err := s.Store.TrafficBillingSettings()
	if err != nil || len(updated.History) != 1 || updated.History[0].Actor == "" {
		t.Fatal("missing operator audit", err)
	}
}

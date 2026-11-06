package control

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestServerBillingAPIExpiryValidationRenewalCASAndAuthorization(t *testing.T) {
	s, h := newServer(t)
	installer := &monitoringInstaller{}
	s.runner = installer
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	s.Store.Now = func() time.Time { return now }
	cookie, csrf := loginAdmin(t, s, h)
	body := map[string]any{"name": "billing-vps", "sshHost": "203.0.113.70", "password": "only-for-enrollment"}
	for _, expiry := range []any{nil, now.Format(time.RFC3339), now.Add(30 * time.Minute).Format(time.RFC3339)} {
		body["expiresAt"] = expiry
		if w := request(h, "POST", "/api/v1/servers", body, cookie, csrf); w.Code != 400 {
			t.Fatalf("bad expiry accepted: %s", w.Body.String())
		}
	}
	if installer.checks != 0 {
		t.Fatal("invalid expiry performed SSH")
	}
	expires := now.Add(24 * time.Hour)
	body["expiresAt"] = expires.Format(time.RFC3339)
	w := request(h, "POST", "/api/v1/servers", body, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var v model.Server
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Billing.Status != "active" || v.Billing.ExpiresAt == nil || !v.Billing.ExpiresAt.Equal(expires) {
		t.Fatal("creation lost expiry", v.Billing)
	}
	path := "/api/v1/servers/" + v.ID + "/renewals"
	renew := map[string]any{"expectedRevision": v.Revision, "requestId": "api-renewal-123", "months": 2}
	if w = request(h, "POST", path, renew, "", ""); w.Code != 401 {
		t.Fatal("anonymous renewal accepted")
	}
	if w = request(h, "POST", path, renew, cookie, ""); w.Code != 403 {
		t.Fatal("renewal skipped CSRF")
	}
	if w = request(s.PublicHandler(), "POST", path, renew, cookie, csrf); w.Code != http.StatusNotFound {
		t.Fatal("renewal on public listener")
	}
	w = request(h, "POST", path, renew, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var first struct {
		Server  model.Server        `json:"server"`
		Renewal model.ServerRenewal `json:"renewal"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	admin, _, _, _ := s.Store.Admin()
	if first.Renewal.Kind != "advance" || first.Renewal.Actor != admin || !first.Server.Billing.ExpiresAt.Equal(expires.Add(60*24*time.Hour)) || installer.checks != 1 {
		t.Fatal("renewal failed or ran SSH", first)
	}
	w = request(h, "POST", path, renew, cookie, csrf)
	if w.Code != 200 {
		t.Fatal("retry failed", w.Body.String())
	}
	var replay struct {
		Renewal model.ServerRenewal `json:"renewal"`
	}
	json.Unmarshal(w.Body.Bytes(), &replay)
	if replay.Renewal.ID != first.Renewal.ID {
		t.Fatal("retry extended twice")
	}
	renew["requestId"] = "new-stale-request"
	if w = request(h, "POST", path, renew, cookie, csrf); w.Code != 409 {
		t.Fatal("stale revision accepted")
	}
	w = request(h, "GET", path, nil, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var history []model.ServerRenewal
	json.Unmarshal(w.Body.Bytes(), &history)
	if len(history) != 2 || history[0].Kind != "advance" || history[1].Kind != "initial" {
		t.Fatal("wrong renewal history", history)
	}
	if w = request(h, "GET", path, nil, "", ""); w.Code != 401 {
		t.Fatal("anonymous history exposed")
	}
	now = *first.Server.Billing.ExpiresAt
	cookie, csrf = loginAdmin(t, s, h)
	renew["expectedRevision"] = first.Server.Revision
	if w = request(h, "POST", path, renew, cookie, csrf); w.Code != 400 {
		t.Fatal("expired renewal defaulted months")
	}
	delete(renew, "months")
	renew["expiresAt"] = now.Add(12 * time.Hour).Format(time.RFC3339)
	w = request(h, "POST", path, renew, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestServerBillingSettingsCanOnlyInitializeExpiryWithoutSSH(t *testing.T) {
	s, h := newServer(t)
	installer := &monitoringInstaller{}
	s.runner = installer
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	s.Store.Now = func() time.Time { return now }
	cookie, csrf := loginAdmin(t, s, h)
	v, err := s.Store.CreateServer(model.Server{Name: "old-unconfigured", SSHHost: "203.0.113.20"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/servers/" + v.ID + "/settings"
	expiry := now.Add(24 * time.Hour)
	body := map[string]any{"name": v.Name, "bandwidthMbps": nil, "monthlyQuotaBytes": nil, "expectedRevision": v.Revision, "expiresAt": expiry.Format(time.RFC3339)}
	w := request(h, "PATCH", path, body, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Billing.Status != "active" || installer.checks != 0 {
		t.Fatal("first expiry required SSH")
	}
	body["expectedRevision"] = v.Revision
	body["expiresAt"] = expiry.Add(24 * time.Hour).Format(time.RFC3339)
	if w = request(h, "PATCH", path, body, cookie, csrf); w.Code != 409 {
		t.Fatal("settings overwrote paid expiry")
	}
	delete(body, "expiresAt")
	body["name"] = "renamed"
	w = request(h, "PATCH", path, body, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil || !v.Billing.ExpiresAt.Equal(expiry) {
		t.Fatal("ordinary edit lost billing", err)
	}
}

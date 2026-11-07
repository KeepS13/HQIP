package control

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func TestServerExpiryCorrectionAPIRequiresAuthorizationAndPreservesAccounting(t *testing.T) {
	s, h := newServer(t)
	installer := &monitoringInstaller{}
	s.runner = installer
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	s.Store.Now = func() time.Time { return now }
	cookie, csrf := loginAdmin(t, s, h)
	v, err := s.Store.CreateServer(model.Server{Name: "expiry-api", SSHHost: "203.0.113.20"})
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(40 * 24 * time.Hour)
	v, err = s.Store.UpdateServerSettings(v.ID, v.Name, nil, nil, v.Revision, store.ServerBillingSetup{ExpiresAt: &expires, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/servers/" + v.ID + "/expiry"
	newExpiry := now.Add(45 * 24 * time.Hour)
	body := map[string]any{"expectedRevision": v.Revision, "requestId": "expiry-api-request", "expiresAt": newExpiry.Format(time.RFC3339)}
	if w := request(h, "PATCH", path, body, "", ""); w.Code != 401 {
		t.Fatal("anonymous expiry edit accepted", w.Code)
	}
	if w := request(h, "PATCH", path, body, cookie, ""); w.Code != 403 {
		t.Fatal("expiry edit skipped CSRF", w.Code)
	}
	if w := request(s.PublicHandler(), "PATCH", path, body, cookie, csrf); w.Code != http.StatusNotFound {
		t.Fatal("public expiry edit exposed", w.Code)
	}
	w := request(h, "PATCH", path, body, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Server model.Server        `json:"server"`
		Change model.ServerRenewal `json:"change"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	admin, _, _, _ := s.Store.Admin()
	if result.Server.Billing.CycleID != v.Billing.CycleID || !result.Server.Billing.CycleStartedAt.Equal(*v.Billing.CycleStartedAt) || result.Server.Billing.NextResetAt == nil || !result.Server.Billing.NextResetAt.Equal(now.Add(15*24*time.Hour)) || result.Change.Kind != "correction" || result.Change.Actor != admin || installer.checks != 0 {
		t.Fatal("wrong correction or SSH call", result, installer.checks)
	}
	w = request(h, "PATCH", path, body, cookie, csrf)
	if w.Code != 200 {
		t.Fatal("retry failed", w.Body.String())
	}
	var replay struct {
		Change model.ServerRenewal `json:"change"`
	}
	json.Unmarshal(w.Body.Bytes(), &replay)
	if replay.Change.ID != result.Change.ID {
		t.Fatal("retry duplicated correction")
	}
	body["requestId"] = "expiry-api-stale"
	if w = request(h, "PATCH", path, body, cookie, csrf); w.Code != 409 {
		t.Fatal("stale expiry version accepted", w.Body.String())
	}
	body["expectedRevision"] = result.Server.Revision
	for _, bad := range []any{nil, "", now.Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)} {
		body["expiresAt"] = bad
		if w = request(h, "PATCH", path, body, cookie, csrf); w.Code != 400 {
			t.Fatal("invalid expiry accepted", w.Code, w.Body.String())
		}
	}
	w = request(h, "GET", "/api/v1/servers/"+v.ID+"/renewals", nil, cookie, "")
	var history []model.ServerRenewal
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &history) != nil || len(history) != 2 || history[0].Kind != "correction" || history[1].Kind != "initial" {
		t.Fatal("history lost correction", w.Body.String())
	}
}

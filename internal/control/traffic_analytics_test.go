package control

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func TestTrafficAnalyticsAPIAuthenticationValidationAndOverviewCompatibility(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	now := time.Date(2026, 11, 16, 16, 0, 0, 0, store.Shanghai)
	s.Store.Now = func() time.Time { return now }
	n, err := s.Store.CreateNode(model.Node{Name: "analytics", SSHHost: "203.0.113.10", SSHUser: "root", SSHPort: 22, PublicIP: "203.0.113.10", Domain: "analytics.example.com", Port: 443}, "ssh")
	if err != nil {
		t.Fatal(err)
	}
	d, err := createAssignedDevice(t, s, "analytics")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Report(n.ID, model.AgentReport{CoreHealthy: true, Daily: []model.UsageBucket{{DeviceID: d.ID, Start: store.DayStart(now), Upload: 123, Download: 456}}}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/traffic/analytics?from=2026-11-16&to=2026-11-16&serverId=" + n.ServerID
	w := request(h, http.MethodGet, path, nil, cookie, csrf)
	var out model.TrafficAnalytics
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Summary.Total != 579 || out.ContributorKind != "node" || len(out.Days) != 1 || !out.IncludesToday {
		t.Fatalf("daily analytics API: %d %s", w.Code, w.Body.String())
	}
	for _, check := range []struct {
		query string
		code  int
	}{{"?from=2026-11-16", 400}, {"?from=2026-11-17&to=2026-11-16", 400}, {"?serverId=missing", 404}, {"", 200}} {
		w = request(h, http.MethodGet, "/api/v1/traffic/analytics"+check.query, nil, cookie, csrf)
		if w.Code != check.code {
			t.Fatalf("%s: %d %s", check.query, w.Code, w.Body.String())
		}
	}
	if w = request(h, http.MethodGet, path, nil, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("analytics must require admin authentication: %d", w.Code)
	}
	w = request(h, http.MethodGet, "/api/v1/overview", nil, cookie, csrf)
	var overview struct {
		Runtime *model.RuntimeOverview `json:"runtime"`
		Usage   model.UsageTotals      `json:"usage"`
		Traffic []model.TrafficPoint   `json:"traffic"`
		Devices []model.Device         `json:"devices"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &overview) != nil || overview.Runtime == nil || overview.Runtime.OnlineStatus != "complete" || overview.Usage.MonthDownload != 456 || overview.Traffic == nil || len(overview.Devices) != 1 {
		t.Fatalf("overview compatibility: %d %s", w.Code, w.Body.String())
	}
}

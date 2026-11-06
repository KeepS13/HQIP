package control

import (
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
)

type monitoringInstaller struct {
	managedInstaller
	checks int
}

func (i *monitoringInstaller) CheckServer(ctx context.Context, v model.Server, password string, pin provision.PinFunc) (provision.ServerInspection, error) {
	i.checks++
	return i.managedInstaller.CheckServer(ctx, v, password, pin)
}

func TestServerCapacityAPIIsolationAndConcurrency(t *testing.T) {
	s, h := newServer(t)
	i := &monitoringInstaller{}
	s.runner = i
	cookie, csrf := loginAdmin(t, s, h)
	w := request(h, "POST", "/api/v1/servers", map[string]any{
		"name": "VPS", "sshHost": "203.0.113.10", "password": "ephemeral",
		"bandwidthMbps": 250.5, "monthlyQuotaBytes": 1_000_000_000_000,
		"expiresAt": s.Store.Now().Add(30 * 24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339),
	}, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var v model.Server
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.BandwidthMbps == nil || *v.BandwidthMbps != 250.5 || v.MonthlyQuotaBytes == nil || *v.MonthlyQuotaBytes != 1_000_000_000_000 || i.checks != 1 {
		t.Fatal("creation lost capacity or skipped SSH verification")
	}
	a, err := s.Store.CreateAddress(v.ID, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Store.CreateNode(model.Node{ServerID: v.ID, AddressID: a.ID, Name: "node", Domain: "node.example.com", Port: 443}, "ssh")
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	var desiredBefore, desiredAfter int64
	if err := s.Store.DB.QueryRow("SELECT config,desired_revision FROM nodes WHERE id=?", n.ID).Scan(&before, &desiredBefore); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/servers/" + v.ID + "/settings"
	body := map[string]any{"name": "Renamed", "bandwidthMbps": 500, "monthlyQuotaBytes": nil, "expectedRevision": v.Revision}
	if w = request(h, "PATCH", path, body, "", ""); w.Code != 401 {
		t.Fatal("unauthenticated settings accepted")
	}
	if w = request(h, "PATCH", path, body, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("PATCH", path, strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cookie", cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	w = request(h, "PATCH", path, body, cookie, csrf)
	if w.Code != 200 || i.checks != 1 {
		t.Fatalf("settings require SSH or failed: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Name != "Renamed" || *v.BandwidthMbps != 500 || v.MonthlyQuotaBytes != nil {
		t.Fatal("settings not saved")
	}
	if w = request(h, "PATCH", path, body, cookie, csrf); w.Code != 409 {
		t.Fatal("stale revision overwrote settings")
	}
	if err := s.Store.DB.QueryRow("SELECT config,desired_revision FROM nodes WHERE id=?", n.ID).Scan(&after, &desiredAfter); err != nil {
		t.Fatal(err)
	}
	if before != after || desiredBefore != desiredAfter {
		t.Fatal("capacity change mutated node configuration or authorization revision")
	}
	for _, invalid := range []map[string]any{
		{"name": "", "expectedRevision": v.Revision, "bandwidthMbps": nil, "monthlyQuotaBytes": nil},
		{"name": "VPS", "expectedRevision": v.Revision, "bandwidthMbps": 0, "monthlyQuotaBytes": nil},
		{"name": "VPS", "expectedRevision": v.Revision, "bandwidthMbps": nil, "monthlyQuotaBytes": -1},
		{"name": "VPS", "expectedRevision": v.Revision, "bandwidthMbps": nil},
		{"name": "VPS", "expectedRevision": v.Revision, "bandwidthMbps": nil, "monthlyQuotaBytes": nil, "sshHost": "evil.example"},
	} {
		if w = request(h, "PATCH", path, invalid, cookie, csrf); w.Code != 400 {
			t.Fatalf("invalid settings accepted: %v (%d)", invalid, w.Code)
		}
	}
	d, err := s.Store.CreateDevice("client")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetClientAccount(d.ID, "monitor-user", "strong-device-password"); err != nil {
		t.Fatal(err)
	}
	w = bearerRequest(s.PublicHandler(), "POST", "/api/v1/client/login", map[string]string{"username": "monitor-user", "password": "strong-device-password", "installationId": "monitor-0001", "clientName": "test"}, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var tokens model.ClientTokens
	if err = json.Unmarshal(w.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct{ method, path string }{{"PATCH", path}, {"GET", "/api/v1/servers/" + v.ID + "/traffic?period=7d"}} {
		if w = bearerRequest(s.AdminHandler(), endpoint.method, endpoint.path, body, tokens.AccessToken); w.Code != 401 {
			t.Fatal("device token reached server management")
		}
		if w = request(s.PublicHandler(), endpoint.method, endpoint.path, body, cookie, csrf); w.Code != http.StatusNotFound {
			t.Fatal("management endpoint registered on public listener")
		}
	}
}

func TestServerMonitoringAPIKeepsDeletedUsageAndSelectsChartPeriod(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	now := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	s.Store.Now = func() time.Time { return now }
	n, err := s.Store.CreateNode(model.Node{Name: "historical", SSHHost: "203.0.113.10", SSHUser: "root", SSHPort: 22, PublicIP: "203.0.113.10", Domain: "one.example.com", Port: 443}, "ssh")
	if err != nil {
		t.Fatal(err)
	}
	d, err := createAssignedDevice(t, s, "historical device")
	if err != nil {
		t.Fatal(err)
	}
	report := model.AgentReport{CoreHealthy: true,
		Daily: []model.UsageBucket{{DeviceID: d.ID, Start: store.DayStart(now), Upload: 100, Download: 200}},
		Minutes: []model.UsageBucket{
			{DeviceID: d.ID, Start: now.Add(-time.Hour).Unix(), Upload: 10, Download: 20},
			{DeviceID: d.ID, Start: now.Add(-48 * time.Hour).Unix(), Upload: 90, Download: 180},
		},
	}
	if err = s.Store.Report(n.ID, report); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DeleteDevice(d.ID); err != nil {
		t.Fatal(err)
	}
	// Represent a completed removal; the accounting mapping must survive it.
	if _, err = s.Store.DB.Exec("UPDATE nodes SET lifecycle='deleted',managed=0 WHERE id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/servers/" + n.ServerID
	w := request(h, "GET", path, nil, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var detail struct {
		Server    model.Server `json:"server"`
		Nodes     []model.Node `json:"nodes"`
		NodeUsage []model.Node `json:"nodeUsage"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 0 || len(detail.NodeUsage) != 1 || detail.Server.MonthUpload != 100 || detail.Server.TotalDownload != 200 {
		t.Fatalf("deleted node lost contribution: %s", w.Body.String())
	}
	for period, want := range map[string]int64{"24h": 30, "7d": 300} {
		w = request(h, "GET", path+"/traffic?period="+period, nil, cookie, "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var points []model.TrafficPoint
		if err = json.Unmarshal(w.Body.Bytes(), &points); err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, point := range points {
			total += point.Upload + point.Download
		}
		if total != want {
			t.Fatalf("%s returned %d, want %d", period, total, want)
		}
	}
	if w = request(h, "GET", path+"/traffic?period=bogus", nil, cookie, ""); w.Code != 400 {
		t.Fatal("invalid period accepted")
	}
	if w = request(h, "GET", "/api/v1/servers/missing/traffic", nil, cookie, ""); w.Code != 404 {
		t.Fatal("missing server not found check lost")
	}
	w = request(h, "GET", "/api/v1/overview", nil, cookie, "")
	var overview struct {
		Usage model.UsageTotals `json:"usage"`
	}
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &overview); err != nil || overview.Usage.MonthUpload != 100 || overview.Usage.TotalDownload != 200 {
		t.Fatalf("overview lost deleted contributions: %s", w.Body.String())
	}
}

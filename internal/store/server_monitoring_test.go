package store

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func assertServerUsage(t *testing.T, s *Store, id string, want model.UsageTotals) model.Server {
	t.Helper()
	v, err := s.Server(id)
	if err != nil {
		t.Fatal(err)
	}
	got := model.UsageTotals{MonthUpload: v.MonthUpload, MonthDownload: v.MonthDownload, TotalUpload: v.TotalUpload, TotalDownload: v.TotalDownload}
	if got != want {
		t.Fatalf("server usage = %+v, want %+v", got, want)
	}
	return v
}

func TestServerUsageRetainsDeletedNodesDevicesAndLateReports(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 2, 12, 0, 0, 0, Shanghai)
	v, a, b := serverPair(t, s)
	other := newNode(t, s, "another-server")
	d1, d2 := newDevice(t, s, "one"), newDevice(t, s, "two")
	previousMonth := now.AddDate(0, -1, 0)
	report(t, s, a, d1, previousMonth, 100, 200)
	report(t, s, a, d1, *now, 10, 20)
	report(t, s, a, d2, *now, 30, 40)
	report(t, s, b, d1, *now, 50, 60)
	report(t, s, other, d1, *now, 70, 80)
	// Replay and out-of-order counters must not inflate any aggregate.
	report(t, s, a, d1, *now, 10, 20)
	report(t, s, a, d1, *now, 9, 19)
	probe := model.AgentReport{Revision: 1, CoreHealthy: true, Metrics: model.Metrics{UploadRate: 9000, DownloadRate: 12000}, Daily: []model.UsageBucket{{DeviceID: ProbeID(a.ID), Start: DayStart(*now), Upload: 99999, Download: 99999}}}
	if err := s.Report(a.ID, probe); err != nil {
		t.Fatal(err)
	}
	want := model.UsageTotals{MonthUpload: 90, MonthDownload: 120, TotalUpload: 190, TotalDownload: 320}
	assertServerUsage(t, s, v.ID, want)
	for _, check := range []struct {
		period   string
		up, down int64
	}{{"24h", 90, 120}, {"7d", 90, 120}, {"90d", 90, 120}, {"13m", 190, 320}} {
		points, err := s.ServerTraffic(v.ID, check.period)
		if err != nil {
			t.Fatal(err)
		}
		var up, down int64
		for i, p := range points {
			up += p.Upload
			down += p.Download
			if i > 0 && points[i-1].At >= p.At {
				t.Fatal("traffic points not uniquely ordered")
			}
		}
		if up != check.up || down != check.down {
			t.Fatalf("%s chart = %d/%d", check.period, up, down)
		}
	}
	if _, err := s.DeleteDevice(d2.ID); err != nil {
		t.Fatal(err)
	}
	assignment, err := s.Assignment(d1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteNode(a.ID, model.NodeDeleteOptions{ExpectedRevision: a.Revision, Replacements: map[string]model.NodeReplacement{d1.ID: {DefaultNodeID: b.ID, ExpectedRevision: assignment.Revision}}}); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteNodeRetirement(a.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteAddress(v.ID, a.AddressID); err != nil {
		t.Fatal(err)
	}
	after := assertServerUsage(t, s, v.ID, want)
	if after.NodeCount != 1 || after.IPCount != 1 {
		t.Fatalf("active inventory counts = %d/%d", after.NodeCount, after.IPCount)
	}
	// Deleted credentials retain accounting identity; current and previous-month
	// backfill count once, even after the physical address entry is removed.
	report(t, s, a, d2, *now, 35, 48)
	report(t, s, a, d2, *now, 35, 48)
	report(t, s, a, d1, previousMonth, 110, 220)
	want = model.UsageTotals{MonthUpload: 95, MonthDownload: 128, TotalUpload: 205, TotalDownload: 348}
	assertServerUsage(t, s, v.ID, want)
	breakdown, err := s.ServerNodeUsage(v.ID)
	if err != nil || len(breakdown) != 2 {
		t.Fatalf("historical node breakdown: %v %+v", err, breakdown)
	}
	var up, down int64
	deleted := false
	for _, n := range breakdown {
		up += n.TotalUpload
		down += n.TotalDownload
		deleted = deleted || n.ID == a.ID && n.Lifecycle == "deleted"
	}
	if !deleted || up != want.TotalUpload || down != want.TotalDownload {
		t.Fatalf("historical breakdown lost tombstone: %+v", breakdown)
	}
	global, err := s.UsageTotals()
	if err != nil || global != (model.UsageTotals{MonthUpload: 165, MonthDownload: 208, TotalUpload: 275, TotalDownload: 428}) {
		t.Fatalf("global ledger totals: %+v %v", global, err)
	}
	assignment, err = s.Assignment(d1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteNode(b.ID, model.NodeDeleteOptions{ExpectedRevision: b.Revision, Replacements: map[string]model.NodeReplacement{d1.ID: {DefaultNodeID: other.ID, ExpectedRevision: assignment.Revision}}}); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteNodeRetirement(b.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteServer(v.ID, v.Revision); err != nil {
		t.Fatal(err)
	}
	got, err := s.UsageTotals()
	if err != nil || got != global {
		t.Fatalf("deleting server changed global accounting: %+v %v", got, err)
	}
	*now = now.AddDate(1, 2, 0)
	if err = s.Maintenance(); err != nil {
		t.Fatal(err)
	}
	got, err = s.UsageTotals()
	if err != nil || got.MonthUpload != 0 || got.MonthDownload != 0 || got.TotalUpload != global.TotalUpload || got.TotalDownload != global.TotalDownload {
		t.Fatalf("retention changed lifetime totals: %+v %v", got, err)
	}
}

func TestServerUsageShanghaiMonthBoundary(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
	v, a, b := serverPair(t, s)
	d := newDevice(t, s, "boundary")
	october := *now
	report(t, s, a, d, *now, 10, 20)
	report(t, s, b, d, *now, 30, 40)
	assertServerUsage(t, s, v.ID, model.UsageTotals{MonthUpload: 40, MonthDownload: 60, TotalUpload: 40, TotalDownload: 60})
	*now = now.Add(time.Second)
	assertServerUsage(t, s, v.ID, model.UsageTotals{TotalUpload: 40, TotalDownload: 60})
	report(t, s, a, d, october, 15, 25)
	report(t, s, b, d, *now, 7, 9)
	assertServerUsage(t, s, v.ID, model.UsageTotals{MonthUpload: 7, MonthDownload: 9, TotalUpload: 52, TotalDownload: 74})
	global, err := s.UsageTotals()
	if err != nil || global != (model.UsageTotals{MonthUpload: 7, MonthDownload: 9, TotalUpload: 52, TotalDownload: 74}) {
		t.Fatalf("global month boundary: %+v %v", global, err)
	}
}

func TestServerRatesAndHostFreshness(t *testing.T) {
	s, now := fixture(t)
	v, a, b := serverPair(t, s)
	at := *now
	*now = at.Add(-10 * time.Second)
	if err := s.Report(a.ID, model.AgentReport{Revision: 1, CoreHealthy: true, Metrics: model.Metrics{CPUPercent: 11, MemoryUsed: 1, MemoryTotal: 8, DiskUsed: 2, DiskTotal: 16, UploadRate: 10, DownloadRate: 20}}); err != nil {
		t.Fatal(err)
	}
	*now = at.Add(-5 * time.Second)
	if err := s.Report(b.ID, model.AgentReport{Revision: 1, CoreHealthy: true, Metrics: model.Metrics{CPUPercent: 22, MemoryUsed: 3, MemoryTotal: 8, DiskUsed: 4, DiskTotal: 16, UploadRate: 30, DownloadRate: 40}}); err != nil {
		t.Fatal(err)
	}
	*now = at
	v, err := s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.RatesStatus != "complete" || v.ExpectedNodes != 2 || v.ReportingNodes != 2 || v.Metrics.UploadRate != 40 || v.Metrics.DownloadRate != 60 || v.Metrics.CPUPercent != 22 || v.Metrics.MemoryTotal != 8 || v.Metrics.DiskTotal != 16 || !v.MetricsFresh || v.MetricsSourceNodeID != b.ID || v.MetricsReportedAt == nil || !v.MetricsReportedAt.Equal(at.Add(-5*time.Second)) {
		t.Fatalf("complete aggregate: %+v", v)
	}
	// A new heartbeat with failed core sampling is not fresh host telemetry.
	if err = s.Report(b.ID, model.AgentReport{Revision: 1, Metrics: model.Metrics{CPUPercent: 99, MemoryTotal: 99, UploadRate: 999}}); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.RatesStatus != "partial" || v.ReportingNodes != 1 || v.Metrics.UploadRate != 10 || v.MetricsSourceNodeID != a.ID || v.Metrics.CPUPercent != 11 || !v.LastSeen.Equal(at) || !v.MetricsReportedAt.Equal(at.Add(-10*time.Second)) {
		t.Fatalf("unhealthy latest report masquerades as live: %+v", v)
	}
	*now = at.Add(20 * time.Second)
	v, err = s.Server(v.ID)
	if err != nil || v.ReportingNodes != 1 {
		t.Fatalf("30-second boundary should still report: %+v %v", v, err)
	}
	*now = now.Add(time.Second)
	v, err = s.Server(v.ID)
	if err != nil || v.RatesStatus != "unknown" || v.ReportingNodes != 0 || v.MetricsFresh || v.MetricsReportedAt != nil || v.MetricsSourceNodeID != "" || v.Metrics != (model.Metrics{}) || v.LastSeen == nil {
		t.Fatalf("stale metrics presented as current: %+v %v", v, err)
	}
	// A managed node remains expected while retiring, until its stop is acknowledged.
	if _, err = s.DeleteNode(a.ID, model.NodeDeleteOptions{ExpectedRevision: a.Revision}); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil || v.ExpectedNodes != 2 {
		t.Fatalf("retiring node prematurely excluded: %+v %v", v, err)
	}
	desired, err := s.Desired(a.ID)
	if err != nil || desired.Retirement == nil {
		t.Fatalf("retirement command: %v", err)
	}
	if err = s.Report(a.ID, model.AgentReport{Revision: desired.Revision, Retirement: &model.RetirementStatus{ID: desired.Retirement.ID, Stopped: true}}); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil || v.ExpectedNodes != 1 || v.NodeCount != 2 {
		t.Fatalf("confirmed stop still expected: %+v %v", v, err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET managed=0 WHERE id=?", b.ID); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil || v.ExpectedNodes != 0 || v.RatesStatus != "none" || v.ReportingNodes != 0 {
		t.Fatalf("unmanaged node counted as expected: %+v %v", v, err)
	}
}

func TestServerSettingsAreIndependentAndRevisionProtected(t *testing.T) {
	s, _ := fixture(t)
	v, a, _ := serverPair(t, s)
	var nodeBefore string
	if err := s.DB.QueryRow("SELECT config FROM nodes WHERE id=?", a.ID).Scan(&nodeBefore); err != nil {
		t.Fatal(err)
	}
	bandwidth, quota := 125.5, int64(1000000000)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateServerSettings(v.ID, "capacity", &bandwidth, &quota, v.Revision)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent settings = %d successes, %d conflicts", success, conflict)
	}
	got, err := s.Server(v.ID)
	if err != nil || got.Revision != 2 || got.SSHHost != v.SSHHost || got.HostKey != v.HostKey || got.MachineID != v.MachineID || got.BandwidthMbps == nil || *got.BandwidthMbps != bandwidth || got.MonthlyQuotaBytes == nil || *got.MonthlyQuotaBytes != quota {
		t.Fatalf("settings changed identity: %+v %v", got, err)
	}
	var nodeAfter string
	if err = s.DB.QueryRow("SELECT config FROM nodes WHERE id=?", a.ID).Scan(&nodeAfter); err != nil || nodeAfter != nodeBefore {
		t.Fatalf("server settings changed node quota or SSH: %v", err)
	}
	if _, err = s.UpdateServer(v, v.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale SSH update overwrote settings: %v", err)
	}
	// A fresh SSH edit preserves capacity even if its form omitted those fields.
	got.SSHHost, got.BandwidthMbps, got.MonthlyQuotaBytes = "new-ssh.example.com", nil, nil
	got.NodeCount, got.MonthUpload, got.Metrics.CPUPercent = 999, 999, 99
	got, err = s.UpdateServer(got, got.Revision)
	if err != nil || got.BandwidthMbps == nil || *got.BandwidthMbps != bandwidth || got.MonthlyQuotaBytes == nil || *got.MonthlyQuotaBytes != quota || got.NodeCount != 2 || got.MonthUpload != 0 {
		t.Fatalf("SSH edit lost capacity or persisted derived fields: %+v %v", got, err)
	}
	var raw string
	if err = s.DB.QueryRow("SELECT config FROM servers WHERE id=?", v.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"metrics", "lastSeen", "addresses", "nodeCount", "monthUpload", "ratesStatus", "metricsReportedAt"} {
		if _, ok := stored[field]; ok {
			t.Fatalf("computed field %q persisted", field)
		}
	}
	got, err = s.UpdateServerSettings(v.ID, " unlimited ", nil, nil, got.Revision)
	if err != nil || got.Name != "unlimited" || got.BandwidthMbps != nil || got.MonthlyQuotaBytes != nil {
		t.Fatalf("explicit unlimited failed: %+v %v", got, err)
	}
}

func TestServerSettingsValidationAndLegacyDefaults(t *testing.T) {
	s, _ := fixture(t)
	v, _, _ := serverPair(t, s)
	for _, bandwidth := range []float64{0, -1, math.NaN(), math.Inf(1), 1e9 + 1} {
		if _, err := s.UpdateServerSettings(v.ID, v.Name, &bandwidth, nil, v.Revision); err == nil {
			t.Fatalf("invalid bandwidth accepted: %v", bandwidth)
		}
		if _, err := s.CreateServer(model.Server{Name: "bad", SSHHost: "bad.example.com", BandwidthMbps: &bandwidth}); err == nil {
			t.Fatalf("invalid initial bandwidth accepted: %v", bandwidth)
		}
	}
	for _, quota := range []int64{0, -1, 1e18 + 1} {
		if _, err := s.UpdateServerSettings(v.ID, v.Name, nil, &quota, v.Revision); err == nil {
			t.Fatalf("invalid quota accepted: %d", quota)
		}
	}
	if _, err := s.UpdateServerSettings(v.ID, " ", nil, nil, v.Revision); err == nil {
		t.Fatal("blank name accepted")
	}
	for _, name := range []string{strings.Repeat("中", 81), "line\nname", "return\rname", "zero\x00name"} {
		if _, err := s.UpdateServerSettings(v.ID, name, nil, nil, v.Revision); err == nil {
			t.Fatalf("invalid settings name accepted: %q", name)
		}
		if _, err := s.CreateServer(model.Server{Name: name, SSHHost: "name.example.com"}); err == nil {
			t.Fatalf("invalid creation name accepted: %q", name)
		}
		candidate := v
		candidate.Name = name
		if _, err := s.UpdateServer(candidate, v.Revision); err == nil {
			t.Fatalf("invalid SSH-edit name accepted: %q", name)
		}
	}
	chinese := strings.Repeat("中", 80)
	created, err := s.CreateServer(model.Server{Name: chinese, SSHHost: "unicode.example.com"})
	if err != nil {
		t.Fatalf("80-character Chinese name rejected: %v", err)
	}
	created, err = s.UpdateServerSettings(created.ID, chinese, nil, nil, created.Revision)
	if err != nil {
		t.Fatalf("Chinese settings rejected: %v", err)
	}
	if _, err = s.UpdateServer(created, created.Revision); err != nil {
		t.Fatalf("Chinese SSH edit rejected: %v", err)
	}
	if _, err := s.DB.Exec("UPDATE servers SET config=json_remove(config,'$.bandwidthMbps','$.monthlyQuotaBytes') WHERE id=?", v.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Server(v.ID)
	if err != nil || got.BandwidthMbps != nil || got.MonthlyQuotaBytes != nil || got.Revision != v.Revision {
		t.Fatalf("old config did not default to unlimited: %+v %v", got, err)
	}
	if _, err = s.UpdateServerSettings("missing", "name", nil, nil, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing settings target: %v", err)
	}
	if _, err = s.ServerTraffic("missing", "24h"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing traffic target: %v", err)
	}
	if _, err = s.ServerNodeUsage("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing breakdown target: %v", err)
	}
}

func TestServerAlertsPreserveIndependentQuotasAndDeduplicateDisk(t *testing.T) {
	s, _ := fixture(t)
	quota := int64(100)
	metrics := model.Metrics{DiskTotal: 1 << 30, DiskUsed: (1 << 30) - 100}
	v := model.Server{ID: "server", Name: "physical", MonthlyQuotaBytes: &quota, MonthUpload: 40, MonthDownload: 40, MetricsFresh: true, Metrics: metrics}
	nodes := []model.Node{
		{ID: "a", ServerID: v.ID, Name: "a", MonthlyQuotaBytes: &quota, MonthUpload: 100, Metrics: metrics},
		{ID: "b", ServerID: v.ID, Name: "b", MonthlyQuotaBytes: &quota, MonthUpload: 80, Metrics: metrics},
	}
	alerts := append(s.Alerts(nodes), s.ServerAlerts([]model.Server{v})...)
	disk, nodeQuota, serverQuota := 0, 0, 0
	for _, alert := range alerts {
		if strings.Contains(alert.Message, "磁盘") {
			disk++
			if alert.ServerID != v.ID || alert.NodeID != "" {
				t.Fatalf("disk alert points to duplicated node: %+v", alert)
			}
		} else if alert.ServerID == v.ID {
			serverQuota++
			if alert.Level != "warning" || !strings.Contains(alert.Message, "80%") {
				t.Fatalf("80%% server alert: %+v", alert)
			}
		} else if alert.NodeID != "" {
			nodeQuota++
		}
	}
	if disk != 1 || nodeQuota != 2 || serverQuota != 1 {
		t.Fatalf("unexpected alerts: %+v", alerts)
	}
	v.MonthDownload = 60
	v.MetricsFresh = false
	alerts = s.ServerAlerts([]model.Server{v})
	if len(alerts) != 1 || alerts[0].Level != "danger" || !strings.Contains(alerts[0].Message, "连接保持正常") {
		t.Fatalf("100%% is advisory and stale disk must not alert: %+v", alerts)
	}
	v.MonthlyQuotaBytes = nil
	if got := s.ServerAlerts([]model.Server{v}); len(got) != 0 {
		t.Fatalf("unlimited generated quota alert: %+v", got)
	}
}

func TestServerMonitoringPropagatesDataErrors(t *testing.T) {
	t.Run("ledger", func(t *testing.T) {
		s, _ := fixture(t)
		v, _, _ := serverPair(t, s)
		if _, err := s.DB.Exec("DROP TABLE daily"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Server(v.ID); err == nil {
			t.Fatal("server hid ledger failure")
		}
		if _, err := s.UsageTotals(); err == nil {
			t.Fatal("global usage hid ledger failure")
		}
		if _, err := s.ServerTraffic(v.ID, "13m"); err == nil {
			t.Fatal("chart hid ledger failure")
		}
		if _, err := s.ServerNodeUsage(v.ID); err == nil {
			t.Fatal("breakdown hid ledger failure")
		}
	})
	t.Run("metrics", func(t *testing.T) {
		s, now := fixture(t)
		v, a, _ := serverPair(t, s)
		if _, err := s.DB.Exec("UPDATE nodes SET managed=1,core_healthy=1,last_seen=?,metrics='invalid-json' WHERE id=?", now.Unix(), a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Server(v.ID); err == nil {
			t.Fatal("server hid invalid live metrics")
		}
	})
}

func TestServerMonitoringRejectsImpossibleTelemetry(t *testing.T) {
	s, now := fixture(t)
	v, a, b := serverPair(t, s)
	if err := s.Report(a.ID, model.AgentReport{Revision: 1, CoreHealthy: true, Metrics: model.Metrics{CPUPercent: 15, UploadRate: 10, DownloadRate: 20}}); err != nil {
		t.Fatal(err)
	}
	for _, metrics := range []model.Metrics{
		{UploadRate: 1e308},
		{DownloadRate: 1e308},
		{UploadRate: -1},
		{DownloadRate: -1},
	} {
		if _, err := s.DB.Exec("UPDATE nodes SET managed=1,core_healthy=1,last_seen=?,metrics=? WHERE id=?", now.Unix(), jsonString(metrics), b.ID); err != nil {
			t.Fatal(err)
		}
		got, err := s.Server(v.ID)
		if err != nil || got.RatesStatus != "partial" || got.ExpectedNodes != 2 || got.ReportingNodes != 1 || got.Metrics.UploadRate != 10 || got.Metrics.DownloadRate != 20 || got.MetricsSourceNodeID != a.ID {
			t.Fatalf("impossible telemetry was included: %+v %v", got, err)
		}
		if _, err = json.Marshal(got); err != nil {
			t.Fatalf("server response cannot encode: %v", err)
		}
	}
	if _, err := s.DB.Exec("UPDATE nodes SET metrics=?", jsonString(model.Metrics{UploadRate: 1e308, DownloadRate: 1e308})); err != nil {
		t.Fatal(err)
	}
	got, err := s.Server(v.ID)
	if err != nil || got.RatesStatus != "unknown" || got.ReportingNodes != 0 || got.MetricsFresh || got.Metrics != (model.Metrics{}) {
		t.Fatalf("all invalid telemetry appeared healthy: %+v %v", got, err)
	}
	if _, err = json.Marshal(got); err != nil {
		t.Fatalf("invalid-rate response cannot encode: %v", err)
	}
	for _, metrics := range []model.Metrics{
		{UploadRate: math.NaN()}, {DownloadRate: math.Inf(1)},
		{CPUPercent: math.NaN()}, {CPUPercent: math.Inf(-1)},
	} {
		if validServerMetrics(metrics) {
			t.Fatalf("non-finite telemetry accepted: %+v", metrics)
		}
	}
}

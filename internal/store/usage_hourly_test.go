package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func hourlyReportFor(t *testing.T, s *Store, n model.Node, d model.Device, now time.Time) model.AgentReport {
	t.Helper()
	desired, err := s.Desired(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	since, through := now.Add(-9*24*time.Hour).Truncate(time.Hour), now
	return model.AgentReport{
		At: now, Revision: desired.Revision, CoreHealthy: true,
		HourlySince: &since, HourlyThrough: &through, Capabilities: []string{"agent-v2", "hourly-usage-v1"},
		Hours: []model.UsageBucket{{DeviceID: d.ID, Start: now.Truncate(time.Hour).Unix(), Upload: 100, Download: 200}},
	}
}

func TestNativeHourlyIngestionIsDistinctAndIdempotent(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "native")
	d := newDevice(t, s, "accounting")
	r := hourlyReportFor(t, s, n, d, *now)
	r.Daily = []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(*now), Upload: 100, Download: 200}}
	r.Minutes = []model.UsageBucket{{DeviceID: d.ID, Start: now.Truncate(time.Minute).Unix(), Upload: 100, Download: 200}}
	r.Hours = append(r.Hours,
		model.UsageBucket{DeviceID: ProbeID(n.ID), Start: now.Truncate(time.Hour).Unix(), Upload: 99999},
		model.UsageBucket{DeviceID: "unknown-identity", Start: now.Truncate(time.Hour).Unix(), Upload: 99999},
		model.UsageBucket{DeviceID: d.ID, Start: now.Add(-8 * 24 * time.Hour).Truncate(time.Hour).Unix(), Upload: 50, Download: 70})
	for i := 0; i < 2; i++ {
		if err := s.Report(n.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	var up, down, derivedUp, derivedDown, lifetimeUp, lifetimeDown int64
	if err := s.DB.QueryRow("SELECT SUM(upload),SUM(download) FROM native_hourly WHERE node_id=?", n.ID).Scan(&up, &down); err != nil || up != 150 || down != 270 {
		t.Fatalf("hourly replay/probe filtering failed: %d/%d %v", up, down, err)
	}
	if err := s.DB.QueryRow("SELECT SUM(upload),SUM(download) FROM hourly WHERE node_id=?", n.ID).Scan(&derivedUp, &derivedDown); err != nil || derivedUp != 100 || derivedDown != 200 {
		t.Fatalf("native hours were added to minute-derived hours: %d/%d %v", derivedUp, derivedDown, err)
	}
	if err := s.DB.QueryRow("SELECT upload,download FROM lifetime WHERE node_id=? AND device_id=?", n.ID, d.ID).Scan(&lifetimeUp, &lifetimeDown); err != nil || lifetimeUp != 100 || lifetimeDown != 200 {
		t.Fatalf("native hours charged lifetime twice: %d/%d %v", lifetimeUp, lifetimeDown, err)
	}
	r.Daily, r.Minutes = nil, nil
	r.Hours = []model.UsageBucket{{DeviceID: d.ID, Start: now.Truncate(time.Hour).Unix(), Upload: 90, Download: 240, Uncertain: true}}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	r.Hours[0].Upload, r.Hours[0].Download, r.Hours[0].Uncertain = 1, 1, false
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	var uncertain bool
	if err := s.DB.QueryRow("SELECT upload,download,uncertain FROM native_hourly WHERE node_id=? AND device_id=? AND start=?", n.ID, d.ID, r.Hours[0].Start).Scan(&up, &down, &uncertain); err != nil || up != 100 || down != 240 || !uncertain {
		t.Fatalf("lower replay reversed counters or uncertainty: %d/%d/%v %v", up, down, uncertain, err)
	}
	node, err := s.Node(n.ID)
	if err != nil || node.TotalUpload != 100 || node.TotalDownload != 200 {
		t.Fatal("hours-only report changed lifetime totals")
	}
}

func TestNativeHourlyReportDoesNotDoubleChargeSubscriptions(t *testing.T) {
	s, now, user, _, node, password := portalFixture(t, 1, nil)
	lease := acquire(t, s, node, password, "h")
	_ = renew(t, s, lease, 80, 120, false)
	desired, _ := s.Desired(node.ID)
	since, through := now.Truncate(time.Hour), *now
	r := model.AgentReport{At: *now, Revision: desired.Revision, CoreHealthy: true, HourlySince: &since, HourlyThrough: &through,
		Daily: []model.UsageBucket{{DeviceID: lease.AccountingID, Start: DayStart(*now), Upload: 80, Download: 120}},
		Hours: []model.UsageBucket{{DeviceID: lease.AccountingID, Start: since.Unix(), Upload: 80, Download: 120}}}
	for i := 0; i < 3; i++ {
		if err := s.Report(node.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	ent, err := s.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if err != nil || ent.UsedBytes != 200 {
		t.Fatalf("hourly report double-charged subscription: %+v %v", ent, err)
	}
	// Closure and credential revocation do not erase the durable mapping used
	// by late traffic reports; the subscription is still settled only once.
	_ = renew(t, s, lease, 80, 120, true)
	r.Hours[0].Upload = 90
	if err := s.Report(node.ID, r); err != nil {
		t.Fatal(err)
	}
	ent, _ = s.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if ent.UsedBytes != 200 {
		t.Fatal("late historical hour billed an already-closed lease")
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM native_hourly WHERE node_id=? AND device_id=?", node.ID, lease.AccountingID).Scan(&count); err != nil || count != 1 {
		t.Fatal("closed lease lost its accounting identity")
	}
}

func TestNativeHourlyCoverageIsConservativeAcrossReplayAndCompaction(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "coverage")
	d := newDevice(t, s, "accounting")
	r := hourlyReportFor(t, s, n, d, *now)
	initial := r
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	newSince := now.Add(-time.Hour)
	r.HourlySince = &newSince
	r.HistoryCompacted = true
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	oldThrough := now.Add(-2 * time.Hour)
	initial.HourlyThrough = &oldThrough
	if err := s.Report(n.ID, initial); err != nil {
		t.Fatal(err)
	}
	var since, through int64
	if err := s.DB.QueryRow("SELECT since,through FROM node_hourly_coverage WHERE node_id=?", n.ID).Scan(&since, &through); err != nil || since != newSince.Unix() || through != now.Unix() {
		t.Fatalf("old report widened certainty or regressed latest sample: %d/%d %v", since, through, err)
	}
	// A legacy Agent can still report daily data but cannot fabricate coverage.
	other := newNode(t, s, "legacy")
	if err := s.Report(other.ID, model.AgentReport{}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM node_hourly_coverage WHERE node_id=?", other.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy report claimed native hourly support")
	}
}

func TestNativeHourlyGapsAreDurableAndDoNotEraseEarlierCoverage(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "gap")
	d := newDevice(t, s, "accounting")
	r := hourlyReportFor(t, s, n, d, *now)
	firstThrough := *r.HourlyThrough
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(48 * time.Hour)
	narrow := *now
	r.HourlySince, r.HourlyThrough, r.At = &narrow, &narrow, *now
	r.Hours = nil
	r.HistoryCompacted = true
	for i := 0; i < 2; i++ {
		if err := s.Report(n.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	var count, start, end int64
	if err := s.DB.QueryRow("SELECT COUNT(*),MIN(start),MAX(end) FROM node_hourly_gaps WHERE node_id=?", n.ID).Scan(&count, &start, &end); err != nil || count != 1 || start != firstThrough.Unix() || end != now.Unix() {
		t.Fatalf("rollback gap lost or duplicated: %d %d/%d %v", count, start, end, err)
	}
	var oldHours int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM native_hourly WHERE node_id=?", n.ID).Scan(&oldHours); err != nil || oldHours != 1 {
		t.Fatal("new coverage erased previously acknowledged native hours")
	}
	// Advancing retention within acknowledged coverage is not an outage.
	*now = now.Add(time.Hour)
	since := now.Add(-30 * time.Minute)
	r.HourlySince, r.HourlyThrough = &since, now
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM node_hourly_gaps WHERE node_id=?", n.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("new unobserved interval was not tracked")
	}
	since = now.Add(-time.Hour)
	r.HourlySince = &since
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM node_hourly_gaps WHERE node_id=?", n.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("retention inside known coverage created a spurious gap")
	}
}

func TestNativeHourlyValidationRollsBackAllReportEffects(t *testing.T) {
	cases := map[string]func(*model.AgentReport){
		"missing range":   func(r *model.AgentReport) { r.HourlyThrough = nil },
		"reversed range":  func(r *model.AgentReport) { t := r.HourlySince.Add(-time.Second); r.HourlyThrough = &t },
		"future sample":   func(r *model.AgentReport) { t := r.At.Add(2 * time.Minute); r.HourlyThrough = &t },
		"misaligned hour": func(r *model.AgentReport) { r.Hours[0].Start++ },
		"negative":        func(r *model.AgentReport) { r.Hours[0].Upload = -1 },
		"overflow":        func(r *model.AgentReport) { r.Hours[0].Download = 1<<60 + 1 },
		"empty identity":  func(r *model.AgentReport) { r.Hours[0].DeviceID = "" },
		"too many":        func(r *model.AgentReport) { r.Hours = make([]model.UsageBucket, 4001) },
		"batch overflow": func(r *model.AgentReport) {
			r.Hours = nil
			for i := 0; i < 9; i++ {
				r.Hours = append(r.Hours, model.UsageBucket{DeviceID: fmt.Sprint(i), Start: r.At.Truncate(time.Hour).Unix(), Upload: 1 << 60})
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, now := fixture(t)
			n := newNode(t, s, "invalid")
			d := newDevice(t, s, "accounting")
			r := hourlyReportFor(t, s, n, d, *now)
			r.Daily = []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(*now), Upload: 1234}}
			mutate(&r)
			if err := s.Report(n.ID, r); err == nil {
				t.Fatal("invalid native report accepted")
			}
			var count, seen int64
			if err := s.DB.QueryRow("SELECT COUNT(*) FROM lifetime WHERE node_id=?", n.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected hours committed daily accounting")
			}
			if err := s.DB.QueryRow("SELECT last_seen FROM nodes WHERE id=?", n.ID).Scan(&seen); err != nil || seen != 0 {
				t.Fatal("rejected hours updated node heartbeat")
			}
		})
	}
}

func TestNativeHourlyRetentionDoesNotDeleteAccountingTotals(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "retained")
	d := newDevice(t, s, "accounting")
	r := hourlyReportFor(t, s, n, d, *now)
	r.Daily = []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(*now), Upload: 100, Download: 200}}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	*now = now.AddDate(0, 14, 0)
	newReport := hourlyReportFor(t, s, n, d, *now)
	newReport.Hours = nil
	if err := s.Report(n.ID, newReport); err != nil {
		t.Fatal(err)
	}
	var count, up, down int64
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM native_hourly WHERE node_id=?", n.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired native hour retained")
	}
	if err := s.DB.QueryRow("SELECT upload,download FROM lifetime WHERE node_id=? AND device_id=?", n.ID, d.ID).Scan(&up, &down); err != nil || up != 100 || down != 200 {
		t.Fatal("native-hour cleanup changed lifetime accounting")
	}
}

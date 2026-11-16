package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestTrafficAnalyticsDailyRangeReplayAndDeletedContributions(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 16, 16, 30, 0, 0, Shanghai)
	v, a, b := serverPair(t, s)
	other := newNode(t, s, "traffic-other")
	d := newDevice(t, s, "traffic-device")
	day := func(n int) time.Time { return time.Date(2026, 11, n, 12, 0, 0, 0, Shanghai) }
	report(t, s, a, d, day(14), 100, 200)
	report(t, s, a, d, day(14), 100, 200)
	report(t, s, a, d, day(15), 20, 30)
	report(t, s, b, d, day(15), 10, 40)
	report(t, s, other, d, day(15), 900, 1)
	report(t, s, a, d, day(16), 777, 888)
	if _, err := s.DB.Exec("UPDATE nodes SET lifecycle='deleted',managed=0 WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?)", b.ID, "__probe_test", DayStart(day(15)), 99999, 99999); err != nil {
		t.Fatal(err)
	}
	get := func(server string) model.TrafficAnalytics {
		t.Helper()
		out, err := s.TrafficAnalytics(context.Background(), "2026-11-13", "2026-11-15", server)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := get(v.ID)
	if out.Summary.Total != 400 || out.Summary.Upload != 130 || out.Summary.Download != 270 || out.DayCount != 3 || out.Summary.RecordedDays != 2 || out.IncludesToday || out.Summary.PeakDate != "2026-11-14" || out.Summary.DailyAverage != 400.0/3 {
		t.Fatalf("incorrect range aggregate: %+v", out)
	}
	if out.Days[0].HasData || !out.Days[1].HasData || out.Days[1].Total != 300 || out.Days[2].Total != 100 || len(out.Contributors) != 2 || !out.Contributors[0].Deleted || out.Contributors[0].ID != a.ID {
		t.Fatalf("lost gaps/deleted history: %+v", out)
	}
	var total int64
	for _, p := range out.Contributors {
		total += p.Total
	}
	if total != out.Summary.Total {
		t.Fatal("contributions differ from daily total")
	}
	global := get("")
	if global.Summary.Total != 1301 || global.ContributorKind != "server" || len(global.Contributors) != 2 {
		t.Fatalf("global server grouping: %+v", global)
	}
	// Late cumulative backfill updates only its original day, once.
	report(t, s, a, d, day(14), 110, 220)
	report(t, s, a, d, day(14), 110, 220)
	if updated := get(v.ID); updated.Summary.Total != 430 || updated.Days[1].Total != 330 {
		t.Fatalf("backfill not idempotent: %+v", updated)
	}
}

func TestTrafficAnalyticsBeijingCalendarValidationAndZeroRecords(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2028, 3, 1, 0, 15, 0, 0, Shanghai)
	n := newNode(t, s, "calendar")
	d := newDevice(t, s, "calendar")
	report(t, s, n, d, now.AddDate(0, 0, -1), 0, 0)
	out, err := s.TrafficAnalytics(context.Background(), "2028-02-29", "2028-03-01", "")
	if err != nil || out.DayCount != 2 || !out.IncludesToday || out.Today != "2028-03-01" || !out.Days[0].HasData || out.Days[1].HasData || !out.Summary.HasData || out.Summary.Total != 0 {
		t.Fatalf("zero was confused with missing data: %+v %v", out, err)
	}
	for _, dates := range [][2]string{{"2028-02-30", "2028-03-01"}, {"2027-02-29", "2027-03-01"}, {"2028-03-01", "2028-02-29"}, {"2028-03-01", "2028-03-02"}, {"2027-01-31", "2028-03-01"}, {"2028-3-1", "2028-03-01"}, {"", "2028-03-01"}, {"2028-03-01", ""}} {
		if _, err = s.TrafficAnalytics(context.Background(), dates[0], dates[1], ""); !errors.Is(err, ErrTrafficRange) {
			t.Fatalf("accepted invalid range %v: %v", dates, err)
		}
	}
	if _, err = s.TrafficAnalytics(context.Background(), "2027-02-01", "2028-03-01", ""); err != nil {
		t.Fatal("retention boundary rejected", err)
	}
	defaultRange, err := s.TrafficAnalytics(context.Background(), "", "", "")
	if err != nil || defaultRange.From != "2028-02-24" || defaultRange.To != "2028-03-01" || len(defaultRange.Days) != 7 {
		t.Fatalf("default calendar range: %+v %v", defaultRange, err)
	}
	if _, err = s.TrafficAnalytics(context.Background(), "", "", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing server must fail", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.TrafficAnalytics(ctx, "", "", ""); err == nil {
		t.Fatal("cancelled query became zero usage")
	}
}

func TestRuntimeOverviewDeduplicatesObservedUsersAcrossSubscriptionsAndNodes(t *testing.T) {
	s, _, user, template, n, pass := portalFixture(t, 4, nil)
	a := acquire(t, s, n, pass, "obs")
	b := grantAnother(t, s, user.ID, template, "runtime-second")
	other, err := s.RegisterPortal("runtime-other@example.com", "runtime-password-123")
	if err != nil {
		t.Fatal(err)
	}
	c := grantAnother(t, s, other.ID, template, "runtime-third")
	n2 := newNode(t, s, "runtime-second-node")
	for _, pair := range [][2]string{{n.ID, b.ID}, {n2.ID, a.SubscriptionID}, {n2.ID, c.ID}} {
		if _, err = s.DB.Exec("INSERT INTO node_accounting_identities(node_id,accounting_id,subject_type,subject_id) VALUES(?,?,'subscription',?)", pair[0], "subscription_"+pair[1], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []struct {
		node   model.Node
		online map[string]int
	}{{n, map[string]int{a.AccountingID: 2, "subscription_" + b.ID: 1, ProbeID(n.ID): 99}}, {n2, map[string]int{a.AccountingID: 3, "subscription_" + c.ID: 4}}} {
		if err = s.Report(input.node.ID, model.AgentReport{CoreHealthy: true, Online: input.online, Metrics: model.Metrics{UploadRate: 24, DownloadRate: 28}}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.RuntimeOverview(context.Background())
	if err != nil || out.OnlineUsers != 2 || out.ActiveSubscriptions != 3 || out.Instances != 10 || out.ExpectedNodes != 2 || out.OnlineStatus != "complete" || out.RatesStatus != "complete" || out.UploadRate != 48 || out.DownloadRate != 56 {
		t.Fatalf("runtime identity dedup failed: %+v %v", out, err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET last_seen=? WHERE id=?", s.Now().Add(-31*time.Second).Unix(), n2.ID); err != nil {
		t.Fatal(err)
	}
	out, err = s.RuntimeOverview(context.Background())
	if err != nil || out.OnlineStatus != "partial" || out.RatesStatus != "partial" || out.OnlineUsers != 1 || out.ActiveSubscriptions != 2 || out.Instances != 3 {
		t.Fatalf("stale node counted as live: %+v %v", out, err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET last_seen=?", s.Now().Add(-31*time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	out, err = s.RuntimeOverview(context.Background())
	if err != nil || out.OnlineStatus != "unknown" || out.RatesStatus != "unknown" || out.Instances != 0 || out.LastReportedAt != nil {
		t.Fatalf("all stale must be unknown: %+v %v", out, err)
	}
}

func TestRuntimeOverviewDoesNotCountLoginsLeasesOrUnmappedConnectionsAsUsers(t *testing.T) {
	s, _, user, _, n, pass := portalFixture(t, 4, nil)
	acquire(t, s, n, pass, "one")
	nativeSubscriptionSession(t, s, user.ID, user.Subscriptions[0].ID)
	if err := s.Report(n.ID, model.AgentReport{CoreHealthy: true}); err != nil {
		t.Fatal(err)
	}
	out, err := s.RuntimeOverview(context.Background())
	if err != nil || out.OnlineUsers != 0 || out.Instances != 0 || out.OnlineStatus != "complete" {
		t.Fatalf("leases/logins are not online evidence: %+v %v", out, err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET online=?,metrics=? WHERE id=?", `{"legacy-unknown":2,"__probe_test":5}`, `{"uploadRate":-1}`, n.ID); err != nil {
		t.Fatal(err)
	}
	out, err = s.RuntimeOverview(context.Background())
	if err != nil || out.OnlineUsers != 0 || out.Instances != 2 || out.UnmappedInstances != 2 || out.OnlineStatus != "partial" || out.RatesStatus != "unknown" {
		t.Fatalf("unknown identity/invalid telemetry misreported: %+v %v", out, err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET last_seen=? WHERE id=?", s.Now().Add(time.Second).Unix(), n.ID); err != nil {
		t.Fatal(err)
	}
	out, err = s.RuntimeOverview(context.Background())
	if err != nil || out.OnlineStatus != "unknown" || out.Instances != 0 {
		t.Fatalf("future report accepted: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.RuntimeOverview(ctx); err == nil {
		t.Fatal("database error must not become an empty online count")
	}
}

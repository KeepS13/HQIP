package agent

import (
	"context"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

func TestDirectTelemetryPreservesHY2MissingCounterCoverage(t *testing.T) {
	l := newHourlyLedger(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	hourlyRecord(t, l, now, "hy2", 0, 0)
	s := nodepolicy.Snapshot{ID: "lease", AccountingID: "vless", Upload: 10, Download: 20}
	if _, _, err := l.recordDirect(ctx, now.Add(time.Second), []nodepolicy.Snapshot{s}); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil || b.hourlyThrough == nil || !b.hourlyThrough.Equal(now) {
		t.Fatalf("direct sample claimed unsampled HY2 coverage: %+v %v", b, err)
	}
	assertUsage(t, b.hours, 10, 20) // Known direct bytes still leave the node promptly.
	if _, _, err = l.record(ctx, now.Add(2*time.Second), "restarted-hy2", map[string]counter{}); err != nil {
		t.Fatal(err)
	}
	var uncertain bool
	if err = l.db.QueryRow("SELECT uncertain FROM native_hourly WHERE device_id='account'").Scan(&uncertain); err != nil || !uncertain {
		t.Fatalf("direct telemetry hid the lost HY2 tail: uncertain=%v err=%v", uncertain, err)
	}
}

func TestDirectTelemetryOwnsCoverageBeforeFirstHY2Sample(t *testing.T) {
	l := newHourlyLedger(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	s := nodepolicy.Snapshot{ID: "lease", AccountingID: "vless", Upload: 10}
	for _, at := range []time.Time{now, now.Add(time.Second)} {
		if _, _, err := l.recordDirect(ctx, at, []nodepolicy.Snapshot{s}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := l.batch()
	if err != nil || b.hourlyThrough == nil || !b.hourlyThrough.Equal(now.Add(time.Second)) {
		t.Fatalf("direct-only coverage did not advance: %+v %v", b, err)
	}
	if len(b.hours) != 1 || !b.hours[0].Uncertain {
		t.Fatal("first accumulated direct sample claimed exact timing")
	}
}

func TestDirectTelemetryClockRollbackRetainsCountersForRetry(t *testing.T) {
	l := newHourlyLedger(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	s := nodepolicy.Snapshot{ID: "lease", AccountingID: "vless", Upload: 10, Download: 20}
	if _, _, err := l.recordDirect(ctx, now, []nodepolicy.Snapshot{s}); err != nil {
		t.Fatal(err)
	}
	s.Upload, s.Download, s.Closed = 15, 30, true
	if _, _, err := l.recordDirect(ctx, now.Add(-time.Second), []nodepolicy.Snapshot{s}); err == nil {
		t.Fatal("clock rollback accepted and discarded the final baseline")
	}
	if up, down, err := l.recordDirect(ctx, now.Add(time.Second), []nodepolicy.Snapshot{s}); err != nil || up != 5 || down != 10 {
		t.Fatalf("clock recovery lost or repeated final bytes: %d/%d %v", up, down, err)
	}
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, b.hours, 15, 30)
}

func TestDirectTelemetryLongGapNarrowsCoverage(t *testing.T) {
	l := newHourlyLedger(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	s := nodepolicy.Snapshot{ID: "lease", AccountingID: "vless"}
	if _, _, err := l.recordDirect(ctx, now, []nodepolicy.Snapshot{s}); err != nil {
		t.Fatal(err)
	}
	s.Upload, s.Download = 10, 20
	later := now.Add(48 * time.Hour)
	if _, _, err := l.recordDirect(ctx, later, []nodepolicy.Snapshot{s}); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil || b.hourlySince == nil || !b.hourlySince.Equal(later) || !b.compacted {
		t.Fatalf("long direct gap fabricated complete coverage: %+v %v", b, err)
	}
	if len(b.hours) != 1 || !b.hours[0].Uncertain {
		t.Fatalf("long direct gap invented hourly detail: %+v", b.hours)
	}
	assertUsage(t, b.hours, 10, 20)
}

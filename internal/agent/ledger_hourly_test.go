package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func newHourlyLedger(t *testing.T) *ledger {
	t.Helper()
	l, err := openLedger(filepath.Join(t.TempDir(), "hourly.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.close() })
	return l
}

func hourlyRecord(t *testing.T, l *ledger, at time.Time, generation string, up, down int64) {
	t.Helper()
	if _, _, err := l.record(context.Background(), at, generation, map[string]counter{"account": {up, down}}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeHoursBeginAtSupportedSampleAndSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	l, err := openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	if _, err = l.db.Exec("INSERT INTO baseline(device_id,generation,tx,rx,sampled_at) VALUES('account','core',100,200,?)", before.Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	hourlyRecord(t, l, before, "core", 110, 220)
	b, err := l.batch()
	if err != nil || b.hourlySince == nil || !b.hourlySince.Equal(before) || !b.hourlyThrough.Equal(before) {
		t.Fatalf("upgrade fabricated earlier coverage: %+v %v", b, err)
	}
	assertUsage(t, b.hours, 10, 20)
	if len(b.hours) != 1 || !b.hours[0].Uncertain {
		t.Fatal("first accumulated sample claimed known timing")
	}
	if err = l.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	if err = l.close(); err != nil {
		t.Fatal(err)
	}
	l, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	hourlyRecord(t, l, before.Add(time.Second), "core", 140, 260)
	hourlyRecord(t, l, before.Add(2*time.Second), "core", 140, 260)
	b, err = l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, b.hours, 40, 60)
	assertUsage(t, b.daily, 40, 60)
	if !b.hourlyThrough.Equal(before.Add(2*time.Second)) || !b.hourlySince.Equal(before) {
		t.Fatal("restart lost durable sample coverage")
	}
}

func TestNativeHoursSplitShanghaiBoundaryWithoutFloatLoss(t *testing.T) {
	l := newHourlyLedger(t)
	before := time.Date(2026, 10, 31, 23, 59, 59, 0, shanghai)
	hourlyRecord(t, l, before, "core", 0, 0)
	value := int64(1<<53) + 3
	hourlyRecord(t, l, before.Add(2*time.Second), "core", value, value+2)
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, b.hours, value, value+2)
	if len(b.hours) != 2 || b.hours[0].Upload != value/2 || b.hours[1].Upload != value-value/2 || b.hours[0].Uncertain || b.hours[1].Uncertain {
		t.Fatalf("hour boundary was not conserved: %+v", b.hours)
	}
}

func TestNativeHoursSurviveControlOutageBeyondMinuteRetention(t *testing.T) {
	l := newHourlyLedger(t)
	start := time.Now().UTC().Truncate(time.Hour).Add(-9 * 24 * time.Hour)
	hourlyRecord(t, l, start, "core", 0, 0)
	for i := int64(1); i <= 9*24; i++ {
		hourlyRecord(t, l, start.Add(time.Duration(i)*time.Hour), "core", i*100, i*200)
	}
	if err := l.maintain(start.Add(9*24*time.Hour), maxPendingBytes); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, b.hours, 9*24*100, 9*24*200)
	assertUsage(t, b.daily, 9*24*100, 9*24*200)
	if len(b.hours) != 9*24 || !b.hourlySince.Equal(start) || !b.hourlyThrough.Equal(start.Add(9*24*time.Hour)) {
		t.Fatalf("long control outage lost hours: count=%d %+v", len(b.hours), b)
	}
	var oldestMinute int64
	if err := l.db.QueryRow("SELECT MIN(start) FROM usage WHERE kind='minute'").Scan(&oldestMinute); err != nil || oldestMinute < start.Add(2*24*time.Hour).Unix() {
		t.Fatalf("minute retention did not run: %d %v", oldestMinute, err)
	}
}

func TestHourlyAckCASIncludesUncertaintyAndNewCounts(t *testing.T) {
	l := newHourlyLedger(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)
	hourlyRecord(t, l, now, "core", 0, 0)
	hourlyRecord(t, l, now.Add(time.Second), "core", 100, 200)
	b, _ := l.batch()
	if b.hours[0].Uncertain {
		t.Fatal("ordinary sample unexpectedly uncertain")
	}
	// No new bytes, but a gap changes uncertainty while the request is in flight.
	hourlyRecord(t, l, now.Add(8*time.Second), "core", 100, 200)
	if err := l.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	uncertain, err := l.batch()
	if err != nil || len(uncertain.hours) != 1 || !uncertain.hours[0].Uncertain {
		t.Fatalf("ack erased a newer uncertainty marker: %+v %v", uncertain, err)
	}
	hourlyRecord(t, l, now.Add(9*time.Second), "core", 150, 260)
	if err := l.acknowledge(uncertain); err != nil {
		t.Fatal(err)
	}
	latest, _ := l.batch()
	assertUsage(t, latest.hours, 150, 260)
	if err := l.acknowledge(latest); err != nil {
		t.Fatal(err)
	}
	empty, _ := l.batch()
	if len(empty.hours) != 0 {
		t.Fatal("acknowledged hours stayed dirty")
	}
	if err := l.replay(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	replay, _ := l.batch()
	assertUsage(t, replay.hours, 150, 260)
}

func TestHourlyBacklogDoesNotOverstateCoverageOrSendDailyAhead(t *testing.T) {
	l := newHourlyLedger(t)
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-time.Duration(reportBatchSize) * time.Hour)
	tx, _ := l.db.Begin()
	if _, err := tx.Exec("INSERT INTO hourly_state(id,since,through,generation) VALUES(1,?,?,'core')", start.UnixNano(), end.Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= reportBatchSize; i++ {
		if _, err := tx.Exec("INSERT INTO native_hourly(start,device_id,upload,download,uncertain) VALUES(?,'account',1,2,?)", start.Add(time.Duration(i)*time.Hour).Unix(), i == reportBatchSize); err != nil {
			t.Fatal(err)
		}
	}
	day := end.In(shanghai)
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, shanghai).Unix()
	if _, err := tx.Exec("INSERT INTO usage(kind,start,device_id,upload,download) VALUES('day',?,'account',100,200)", dayStart); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil || len(b.hours) != reportBatchSize || !b.hourlyThrough.Equal(end) || len(b.daily) != 0 {
		t.Fatalf("partial batch overstated coverage or sent its day early: hours=%d through=%v days=%d err=%v", len(b.hours), b.hourlyThrough, len(b.daily), err)
	}
	if err := l.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	last, err := l.batch()
	if err != nil || len(last.hours) != 1 || !last.hours[0].Uncertain || len(last.daily) != 1 || !last.hourlyThrough.Equal(end.Add(time.Minute)) {
		t.Fatalf("last hour did not unlock its daily total: %+v %v", last, err)
	}
}

func TestNativeHourQueueAndRetentionNarrowCoverageExplicitly(t *testing.T) {
	l := newHourlyLedger(t)
	now := time.Now().UTC().Truncate(time.Hour)
	hourlyRecord(t, l, now.Add(-3*time.Hour), "core", 0, 0)
	for i := int64(1); i <= 3; i++ {
		hourlyRecord(t, l, now.Add(time.Duration(i-3)*time.Hour), "core", i*100, i*200)
	}
	if err := l.maintain(now, pendingRowBytes); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil || !b.compacted || len(b.hours) > 1 || !b.hourlySince.Equal(now.Add(-time.Hour)) {
		t.Fatalf("queue silently lost hours: %+v %v", b, err)
	}
	assertUsage(t, b.daily, 300, 600)
	// Beyond 13 months dirty hours may be removed only with an explicit loss
	// marker; dirty authoritative daily/lifetime accounting remains retained.
	if err := l.maintain(now.AddDate(0, 14, 0), maxPendingBytes); err != nil {
		t.Fatal(err)
	}
	b, _ = l.batch()
	if len(b.hours) != 0 || !b.compacted || !b.hourlySince.Equal(*b.hourlyThrough) {
		t.Fatal("expired hourly data retained or claimed as covered")
	}
	assertUsage(t, b.daily, 300, 600)
}

func TestHourlyDowntimeCoreResetAndClockRegressionAreExplicit(t *testing.T) {
	l := newHourlyLedger(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(20 * time.Minute)
	hourlyRecord(t, l, now, "core-a", 0, 0)
	hourlyRecord(t, l, now.Add(time.Second), "core-a", 10, 20)
	later := now.Add(2 * 24 * time.Hour)
	hourlyRecord(t, l, later, "core-a", 100, 200)
	b, _ := l.batch()
	if !b.compacted || !b.hourlySince.Equal(later) || len(b.hours) != 2 || !b.hours[1].Uncertain || b.hours[1].Upload != 90 {
		t.Fatalf("downtime was fabricated as uniform traffic: %+v", b)
	}
	hourlyRecord(t, l, later.Add(time.Second), "core-b", 5, 8)
	b, _ = l.batch()
	assertUsage(t, b.hours, 105, 208)
	if _, _, err := l.record(context.Background(), later, "core-b", map[string]counter{"account": {6, 9}}); err == nil {
		t.Fatal("backward clock advanced the durable baseline")
	}
	hourlyRecord(t, l, later.Add(2*time.Second), "core-b", 6, 9)
	b, _ = l.batch()
	assertUsage(t, b.hours, 106, 209)
}

func TestHourlyCounterOverflowRollsBackSample(t *testing.T) {
	l := newHourlyLedger(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(time.Minute)
	hourlyRecord(t, l, now, "core", 0, 0)
	hourlyRecord(t, l, now.Add(time.Second), "core", hourlyCounterLimit, 0)
	if _, _, err := l.record(context.Background(), now.Add(2*time.Second), "core", map[string]counter{"account": {hourlyCounterLimit + 1, 0}}); err == nil {
		t.Fatal("overflowing hourly total accepted")
	}
	b, _ := l.batch()
	assertUsage(t, b.hours, hourlyCounterLimit, 0)
	assertUsage(t, b.daily, hourlyCounterLimit, 0)
	if !b.hourlyThrough.Equal(now.Add(time.Second)) {
		t.Fatal("failed sample advanced coverage")
	}
}

func TestNativeHourCapabilityAndRetirementDrain(t *testing.T) {
	var received model.AgentReport
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Error(err)
			}
		}
		_ = json.NewEncoder(w).Encode(model.AgentDesired{Revision: 1})
	}))
	defer control.Close()
	a := testAgent(t, "", control.URL)
	now := time.Now().UTC().Truncate(time.Hour)
	hourlyRecord(t, a.ledger, now, "core", 0, 0)
	hourlyRecord(t, a.ledger, now.Add(time.Second), "core", 100, 200)
	if err := a.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(received.Capabilities, "hourly-usage-v1") || len(received.Hours) != 1 || received.HourlySince == nil || received.HourlyThrough == nil {
		t.Fatalf("hourly protocol not sent: %+v", received)
	}
	if _, err := a.ledger.db.Exec("UPDATE native_hourly SET dirty=1"); err != nil {
		t.Fatal(err)
	}
	a.cfg.Managed = true
	a.stopCore = func(context.Context) error { return nil }
	a.disableAgent = func(context.Context) error { return nil }
	prepared := false
	a.prepareRetirement = func(context.Context) error { prepared = true; return nil }
	if err := a.applyDesired(model.AgentDesired{Revision: 1, Retirement: &model.AgentRetirement{ID: "hour-retire", Finalize: true}}); err != nil {
		t.Fatal(err)
	}
	if err := a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prepared || a.cache.Retirement.Final {
		t.Fatal("retirement discarded a pending native hour")
	}
	b, _ := a.ledger.batch()
	if err := a.ledger.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	if err := a.progressRetirement(context.Background()); err != nil || !prepared {
		t.Fatalf("hour acknowledgment did not allow retirement: %v", err)
	}
}

func TestLegacyAgentRollbackCannotMisreadNativeHoursAsMinutes(t *testing.T) {
	l := newHourlyLedger(t)
	now := time.Now().UTC().Truncate(time.Hour)
	hourlyRecord(t, l, now, "core", 0, 0)
	hourlyRecord(t, l, now.Add(time.Second), "core", 100, 200)
	var extra int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM usage WHERE kind NOT IN ('day','minute')").Scan(&extra); err != nil || extra != 0 {
		t.Fatalf("old Agent would interpret native hours as minutes: %d %v", extra, err)
	}
	// A rolled-back Agent can move baseline without native hours. On upgrade
	// again the uncovered interval is visibly uncertain rather than a zero gap.
	if _, err := l.db.Exec("UPDATE baseline SET sampled_at=?,tx=150,rx=260 WHERE device_id='account'", now.Add(2*time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	// This is what the old Agent already charged while native accounting was
	// absent; returning to the new version must not add that difference again.
	if _, err := l.db.Exec("UPDATE totals SET upload=150,download=260 WHERE device_id='account'; UPDATE usage SET upload=150,download=260 WHERE kind='day' AND device_id='account'"); err != nil {
		t.Fatal(err)
	}
	hourlyRecord(t, l, now.Add(2*time.Hour+time.Second), "core", 160, 280)
	b, _ := l.batch()
	assertUsage(t, b.hours, 160, 280)
	assertUsage(t, b.daily, 160, 280)
	if !b.hourlySince.Equal(now.Add(2*time.Hour+time.Second)) || !b.compacted {
		t.Fatal("rollback gap retained a misleading coverage range")
	}
	var up, down int64
	if err := l.db.QueryRow("SELECT upload,download FROM totals WHERE device_id='account'").Scan(&up, &down); err != nil || up != 160 || down != 280 {
		t.Fatalf("native recovery charged daily/lifetime twice: %d/%d %v", up, down, err)
	}
	for _, h := range b.hours {
		if !h.Uncertain {
			t.Fatalf("rollback interval considered exact: %s %+v", fmt.Sprint(h.Start), h)
		}
	}
}

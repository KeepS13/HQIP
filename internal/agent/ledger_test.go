package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestLedgerSurvivesAgentAndCoreRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	l, err := openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, shanghai)
	record := func(generation string, tx, rx int64) {
		t.Helper()
		_, _, err := l.record(context.Background(), now, generation, map[string]counter{"phone": {tx, rx}})
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	record("core-a", 100, 200)
	record("core-a", 100, 200)
	record("core-a", 150, 260)
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	l, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	record("core-a", 175, 300)
	record("core-b", 25, 40)
	record("core-b", 25, 40)
	batch, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, batch.daily, 200, 340)
	assertUsage(t, batch.minutes, 200, 340)
	var tx, rx int64
	if err := l.db.QueryRow("SELECT upload,download FROM totals WHERE device_id='phone'").Scan(&tx, &rx); err != nil {
		t.Fatal(err)
	}
	if tx != 200 || rx != 340 {
		t.Fatalf("lifetime = %d/%d", tx, rx)
	}
}

func TestLedgerAllocatesAcrossShanghaiMonthBoundary(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	before := time.Date(2026, 10, 31, 23, 59, 59, 0, shanghai)
	if _, _, err := l.record(context.Background(), before, "core", map[string]counter{"phone": {0, 0}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.record(context.Background(), before.Add(2*time.Second), "core", map[string]counter{"phone": {200, 400}}); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.daily) != 2 {
		t.Fatalf("days = %+v", b.daily)
	}
	for _, day := range b.daily {
		if day.Upload != 100 || day.Download != 200 {
			t.Fatalf("wrong month attribution: %+v", day)
		}
		if time.Unix(day.Start, 0).In(shanghai).Hour() != 0 {
			t.Fatalf("day not Shanghai midnight: %+v", day)
		}
	}
	assertUsage(t, b.minutes, 200, 400)
}

func TestAcknowledgmentCannotEraseConcurrentSample(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, shanghai)
	if _, _, err := l.record(context.Background(), now, "core", map[string]counter{"a": {100, 200}}); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.record(context.Background(), now.Add(time.Second), "core", map[string]counter{"a": {150, 250}}); err != nil {
		t.Fatal(err)
	}
	if err := l.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	newBatch, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, newBatch.daily, 150, 250)
	if err := l.acknowledge(newBatch); err != nil {
		t.Fatal(err)
	}
	empty, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.daily)+len(empty.minutes) != 0 {
		t.Fatal("acknowledged data is still pending")
	}
}

func TestQueueCompactionPreservesDayAndLifetime(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, shanghai)
	for n := int64(1); n <= 20; n++ {
		if _, _, err := l.record(context.Background(), now.Add(time.Duration(n)*time.Minute), "core", map[string]counter{"a": {n * 100, n * 200}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.maintain(now.Add(time.Hour), 3*pendingRowBytes); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	if !b.compacted || len(b.daily)+len(b.minutes) > 3 {
		t.Fatalf("queue was not compacted: %+v", b)
	}
	assertUsage(t, b.daily, 2000, 4000)
	var tx, rx int64
	if err := l.db.QueryRow("SELECT upload,download FROM totals WHERE device_id='a'").Scan(&tx, &rx); err != nil {
		t.Fatal(err)
	}
	if tx != 2000 || rx != 4000 {
		t.Fatalf("lifetime totals lost: %d/%d", tx, rx)
	}
}

func TestRetentionNeverDropsUnacknowledgedDay(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, shanghai)
	old := now.AddDate(0, -14, 0)
	if _, _, err := l.record(context.Background(), old, "core", map[string]counter{"a": {100, 200}}); err != nil {
		t.Fatal(err)
	}
	if err := l.maintain(now, maxPendingBytes); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, b.daily, 100, 200)
	if len(b.minutes) != 0 || !b.compacted {
		t.Fatal("expired minute history was not compacted")
	}
	if err := l.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	if err := l.maintain(now, maxPendingBytes); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM usage").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("acknowledged expired history retained")
	}
	if err := l.db.QueryRow("SELECT COUNT(*) FROM totals").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("lifetime total removed")
	}
}

func TestRetainedCountersReplayAfterControlDatabaseRestore(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	now := time.Now().In(shanghai)
	if _, _, err := l.record(context.Background(), now, "core", map[string]counter{"phone": {100, 200}}); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	if err := l.maintain(now.Add(2*time.Hour), maxPendingBytes); err != nil {
		t.Fatal(err)
	}
	replay, err := l.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, replay.daily, 100, 200)
	assertUsage(t, replay.minutes, 100, 200)
}

func assertUsage(t *testing.T, buckets []model.UsageBucket, wantTX, wantRX int64) {
	t.Helper()
	var tx, rx int64
	for _, b := range buckets {
		tx += b.Upload
		rx += b.Download
	}
	if tx != wantTX || rx != wantRX {
		t.Fatalf("usage %d/%d, want %d/%d (%+v)", tx, rx, wantTX, wantRX, buckets)
	}
}

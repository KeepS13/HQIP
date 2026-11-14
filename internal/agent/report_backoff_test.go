package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestReportBackoffKeepsConfigSyncAndPendingUsage(t *testing.T) {
	var configs, reports atomic.Int32
	var accept atomic.Bool
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agents/report" {
			reports.Add(1)
			if !accept.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		} else {
			configs.Add(1)
		}
		_ = json.NewEncoder(w).Encode(model.AgentDesired{Revision: 1})
	}))
	defer control.Close()
	a := testAgent(t, "", control.URL)
	now := time.Now().UTC().Truncate(time.Minute)
	a.now = func() time.Time { return now }
	if _, err := a.ledger.db.Exec("INSERT INTO usage(kind,start,device_id,upload,download) VALUES('minute',?,'account',100,200)", now.Unix()); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		if err := a.sync(context.Background()); err == nil {
			t.Fatal("failed report was accepted")
		}
		if delay := a.reportRetryAt.Sub(now); delay < 4*time.Second || delay > 30*time.Second || attempt > 0 && delay < 8*time.Second {
			t.Fatalf("unbounded or missing report backoff: %s", delay)
		}
		before := reports.Load()
		now = now.Add(time.Second)
		_ = a.sync(context.Background())
		if reports.Load() != before || configs.Load() != int32((attempt+1)*2) {
			t.Fatal("backoff must skip only the failed report, not configuration synchronization")
		}
		b, err := a.ledger.batch()
		if err != nil || len(b.minutes) != 1 || b.minutes[0].Upload != 100 {
			t.Fatal("failed/skipped report discarded pending counters", err)
		}
		now = now.Add(31 * time.Second)
	}
	accept.Store(true)
	if err := a.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := a.ledger.batch()
	if err != nil || len(b.minutes) != 0 || !a.reportRetryAt.IsZero() || a.reportBackoff != 0 {
		t.Fatal("successful retry did not acknowledge/reset", err)
	}
	// A success restores the regular cadence immediately.
	before := reports.Load()
	if err := a.sync(context.Background()); err != nil || reports.Load() != before+1 {
		t.Fatal("recovered report remained throttled", err)
	}
}

func TestReportDailyActivityDoesNotStarveMinuteHistory(t *testing.T) {
	l := newHourlyLedger(t)
	tx, err := l.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute).Unix()
	for i := 0; i < 600; i++ {
		if _, err := tx.Exec("INSERT INTO usage(kind,start,device_id,upload,download) VALUES('day',?,?,1,2)", now/86400*86400, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec("INSERT INTO usage(kind,start,device_id,upload,download) VALUES('minute',?,'active',3,4)", now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b, err := l.batch()
	if err != nil || len(b.daily) != 600 || len(b.minutes) != 1 {
		t.Fatal("live daily totals starved minute backlog", len(b.daily), len(b.minutes), err)
	}
}

func TestReportPagesDrainWithoutLosingConcurrentSamples(t *testing.T) {
	l := newHourlyLedger(t)
	now := time.Now().UTC().Truncate(time.Minute)
	const records = 1201
	tx, err := l.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < records; i++ {
		if _, err := tx.Exec("INSERT INTO usage(kind,start,device_id,upload,download) VALUES('minute',?,'account',1,2)", now.Add(time.Duration(i-records)*time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]model.UsageBucket)
	for page := 0; page < 4; page++ {
		b, err := l.batch()
		if err != nil || len(b.minutes) > 500 {
			t.Fatal("history report exceeded bounded page", len(b.minutes), err)
		}
		if len(b.minutes) == 0 {
			break
		}
		for _, bucket := range b.minutes {
			seen[bucket.Start] = bucket
		}
		if page == 0 {
			// A sample arriving during the request must survive its acknowledgment.
			if _, err := l.db.Exec("UPDATE usage SET upload=9,download=10 WHERE kind='minute' AND start=?", b.minutes[0].Start); err != nil {
				t.Fatal(err)
			}
		}
		if err := l.acknowledge(b); err != nil {
			t.Fatal(err)
		}
	}
	remaining, err := l.batch()
	first := now.Add(-records * time.Minute).Unix()
	if err != nil || len(remaining.minutes) != 0 || len(seen) != records || seen[first].Upload != 9 || seen[first].Download != 10 {
		t.Fatal("paged replay lost or stranded cumulative counters", len(seen), seen[first], err)
	}
}

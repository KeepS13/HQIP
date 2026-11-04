package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestFinalLeaseUsagePersistsAcrossRestartAndReplaysOnce(t *testing.T) {
	var online atomic.Bool
	var calls atomic.Int32
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path != "/api/v1/agents/leases/final-lease/renew" {
			t.Error("replay attempted an admission")
		}
		var usage model.LeaseUsage
		_ = json.NewDecoder(r.Body).Decode(&usage)
		if !usage.Closed || usage.UploadBytes != 123 || usage.DownloadBytes != 456 || usage.AppliedRateVersion != 7 {
			t.Error("wrong final usage", usage)
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"leaseId":"final-lease","uploadBytes":123,"downloadBytes":456,"closed":true}`))
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	cfg := a.cfg
	w := admissionRequest(a, "/leases/final-lease/renew", `{"uploadBytes":123,"downloadBytes":456,"closed":true,"appliedRateVersion":7}`, "127.0.0.1:1", testSecret)
	if w.Code != 503 {
		t.Fatal("offline final should report delivery pending")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restored.now = func() time.Time { return time.Now().Add(10 * time.Second) }
	online.Store(true)
	if err = restored.replayLeaseFinals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = restored.replayLeaseFinals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("acknowledged final did not stop retrying", calls.Load())
	}
	var closed, dirty int
	_ = restored.ledger.db.QueryRow("SELECT closed,dirty FROM lease_usage WHERE lease_id='final-lease'").Scan(&closed, &dirty)
	if closed != 1 || dirty != 0 {
		t.Fatal("final tombstone was not acknowledged")
	}
}

func TestLeaseQueueCASAndClosedMonotonicity(t *testing.T) {
	a := testAgent(t, "", "")
	now := time.Now()
	old, err := a.ledger.saveLeaseUsage("one", model.LeaseUsage{UploadBytes: 100, AppliedRateVersion: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	final, err := a.ledger.saveLeaseUsage("one", model.LeaseUsage{UploadBytes: 200, DownloadBytes: 300, AppliedRateVersion: 3, Closed: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ledger.ackLeaseUsage(old); err != nil {
		t.Fatal(err)
	}
	pending, err := a.ledger.pendingLeaseFinals(now)
	if err != nil || len(pending) != 1 || pending[0].Version != final.Version {
		t.Fatal("late old response erased final usage")
	}
	for _, u := range []model.LeaseUsage{{UploadBytes: 200, DownloadBytes: 300, AppliedRateVersion: 3}, {UploadBytes: 199, DownloadBytes: 300, AppliedRateVersion: 3, Closed: true}, {UploadBytes: 200, DownloadBytes: 300, AppliedRateVersion: 2, Closed: true}} {
		if _, err = a.ledger.saveLeaseUsage("one", u, now); err != errLeaseCounterRegression {
			t.Fatal("final/counters/ACK regressed", err)
		}
	}
	if err = a.ledger.ackLeaseUsage(final); err != nil {
		t.Fatal(err)
	}
	pending, _ = a.ledger.pendingLeaseFinals(now)
	if len(pending) != 0 {
		t.Fatal("confirmed final remains pending")
	}
}

func TestLeaseQueueNeverRenewsDeadCoreFromStaleNonfinal(t *testing.T) {
	var calls atomic.Int32
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	if _, err := a.ledger.saveLeaseUsage("crashed", model.LeaseUsage{UploadBytes: 500}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := a.replayLeaseFinals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("background queue revived a crashed core lease")
	}
}

func TestLeaseQueueCapacityNeverPrunesUnconfirmedFinal(t *testing.T) {
	a := testAgent(t, "", "")
	_, err := a.ledger.db.Exec(`WITH RECURSIVE numbers(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<?) INSERT INTO lease_usage(lease_id,upload,download,closed,applied_rate,version,dirty,received) SELECT 'queued-'||n,1,2,1,0,1,1,0 FROM numbers`, maxLeaseQueueRows)
	if err != nil {
		t.Fatal(err)
	}
	if a.ledger.leaseCapacity(time.Now()) == nil {
		t.Fatal("full unconfirmed queue admitted more connections")
	}
	var count int
	_ = a.ledger.db.QueryRow("SELECT COUNT(*) FROM lease_usage").Scan(&count)
	if count != maxLeaseQueueRows {
		t.Fatal("unconfirmed finals were pruned")
	}
	_, _ = a.ledger.db.Exec("UPDATE lease_usage SET dirty=0 WHERE lease_id='queued-1'")
	if err = a.ledger.leaseCapacity(time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = a.ledger.db.QueryRow("SELECT COUNT(*) FROM lease_usage").Scan(&count)
	if count != maxLeaseQueueRows-1 {
		t.Fatal("acknowledged final did not release queue capacity")
	}
}

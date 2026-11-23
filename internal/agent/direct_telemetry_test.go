package agent

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/outbound"
	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

type testDirectMeter struct{ snapshot nodepolicy.Snapshot }

func (c *testDirectMeter) Snapshot() nodepolicy.Snapshot   { return c.snapshot }
func (c *testDirectMeter) Close() error                    { c.snapshot.Closed = true; return nil }
func (*testDirectMeter) TCP(string) (net.Conn, error)      { return nil, net.ErrClosed }
func (*testDirectMeter) UDP() (outbound.PacketConn, error) { return nil, net.ErrClosed }

func TestDirectTelemetrySharesBucketsAndFinalDoesNotDuplicate(t *testing.T) {
	a := testAgent(t, "", "")
	now := time.Now().UTC().Truncate(time.Second)
	a.now = func() time.Time { return now }
	ctx := context.Background()
	if _, _, err := a.ledger.record(ctx, now, "hy2", map[string]counter{"account": {TX: 10, RX: 20}}); err != nil {
		t.Fatal(err)
	}
	meter := &testDirectMeter{snapshot: nodepolicy.Snapshot{ID: "direct-lease", AccountingID: "account", Upload: 100, Download: 200}}
	tracked, err := a.trackDirect(meter)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = a.sampleDirect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a.directOnline()["account"] != 1 {
		t.Fatal("direct online count missing")
	}
	assertTotals := func(up, down int64) {
		t.Helper()
		var gotUp, gotDown int64
		if err := a.ledger.db.QueryRow("SELECT upload,download FROM totals WHERE device_id='account'").Scan(&gotUp, &gotDown); err != nil || gotUp != up || gotDown != down {
			t.Fatal("raw telemetry duplicated or lost", gotUp, gotDown, err)
		}
		var hourUp, hourDown int64
		if err := a.ledger.db.QueryRow("SELECT SUM(upload),SUM(download) FROM native_hourly WHERE device_id='account'").Scan(&hourUp, &hourDown); err != nil || hourUp != up || hourDown != down {
			t.Fatal("hourly telemetry differs", hourUp, hourDown, err)
		}
	}
	assertTotals(110, 220)
	now = now.Add(time.Second)
	meter.snapshot.Upload, meter.snapshot.Download = 130, 260
	if err = tracked.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tracked.Close(); err != nil {
		t.Fatal(err)
	}
	if err = a.sampleDirect(ctx); err != nil {
		t.Fatal(err)
	}
	assertTotals(140, 280)
	if len(a.directOnline()) != 0 || len(a.directSessions) != 0 {
		t.Fatal("final retained online session")
	}
	var baselines int
	_ = a.ledger.db.QueryRow("SELECT COUNT(*) FROM baseline WHERE generation='direct'").Scan(&baselines)
	if baselines != 0 {
		t.Fatal("confirmed direct telemetry leaked baselines")
	}
}

func TestDirectTelemetryContinuesWhenHY2SamplingFails(t *testing.T) {
	a := testAgent(t, "", "")
	now := time.Now().UTC().Truncate(time.Second)
	a.now = func() time.Time { return now }
	a.directSampleAt = now.Add(-time.Second)
	a.generation = func(context.Context) (string, error) { return "", errors.New("HY2 is not running") }
	meter := &testDirectMeter{snapshot: nodepolicy.Snapshot{ID: "lease", AccountingID: "account", Upload: 100, Download: 200}}
	tracked, err := a.trackDirect(meter)
	if err != nil {
		t.Fatal(err)
	}
	defer tracked.Close()
	if err = a.sample(context.Background()); err == nil {
		t.Fatal("HY2 should remain unhealthy")
	}
	a.liveMu.RLock()
	healthy, metrics := a.live.Healthy, a.live.Metrics
	a.liveMu.RUnlock()
	if healthy || metrics.UploadRate != 100 || metrics.DownloadRate != 200 {
		t.Fatal("direct traffic disappeared with HY2", metrics)
	}
	var upload, download int64
	if err = a.ledger.db.QueryRow("SELECT upload,download FROM totals WHERE device_id='account'").Scan(&upload, &download); err != nil || upload != 100 || download != 200 {
		t.Fatal("direct bytes were not durably sampled", err)
	}
}

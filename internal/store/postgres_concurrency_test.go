package store

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestPostgresConcurrentReportReplayAcrossPools(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	s, now := fixture(t)
	n := newNode(t, s, "concurrent-report")
	d := newDevice(t, s, "report-device")
	b, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Now = s.Now
	desired, err := s.Desired(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Daily: []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(*now), Upload: 100, Download: 200}}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 0 {
				db = b
			}
			if err := db.Report(n.ID, r); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var up, down int64
	if err = s.DB.QueryRow("SELECT upload,download FROM lifetime WHERE node_id=? AND device_id=?", n.ID, d.ID).Scan(&up, &down); err != nil || up != 100 || down != 200 {
		t.Fatal("concurrent replay double charged", up, down, err)
	}
}

func TestPostgresMaintenanceLeadershipAcrossPools(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	s, _ := fixture(t)
	b, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	started := make(chan int, 2)
	done := make(chan error, 2)
	var active atomic.Int64
	work := func(id int) func(context.Context) error {
		return func(ctx context.Context) error {
			if active.Add(1) != 1 {
				t.Error("duplicate background owner")
			}
			started <- id
			<-ctx.Done()
			active.Add(-1)
			return nil
		}
	}
	go func() { done <- s.WithMaintenanceLeadership(first, work(1)) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first owner missing")
	}
	go func() { done <- b.WithMaintenanceLeadership(ctx, work(2)) }()
	if err = b.DB.Ping(); err != nil {
		t.Fatal("waiting replica cannot serve", err)
	}
	select {
	case <-started:
		t.Fatal("second owner ran concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	stopFirst()
	select {
	case id := <-started:
		if id != 2 {
			t.Fatal(id)
		}
	case <-ctx.Done():
		t.Fatal("ownership was not transferred")
	}
	cancel()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresSubscriptionReadLoad(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	quota := int64(64 << 20)
	s, _, u, _, n, password := portalFixture(t, 2, &quota)
	b, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Now = s.Now
	lease := acquire(t, s, n, password, "a")
	renew(t, s, lease, 123, 456, false)
	const workers, each = 32, 20
	elapsed := make(chan time.Duration, workers*each)
	var failures atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 0 {
				db = b
			}
			for j := 0; j < each; j++ {
				at := time.Now()
				v, e := db.UserSubscription(u.ID, u.Subscriptions[0].ID)
				elapsed <- time.Since(at)
				if e != nil || v.UsedBytes != 579 {
					failures.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()
	total := time.Since(start)
	close(elapsed)
	times := make([]time.Duration, 0, workers*each)
	for d := range elapsed {
		times = append(times, d)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	var version string
	if err = s.DB.QueryRow("SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("database=%s rows=1 subscription workers=%d requests=%d duration=%s throughput=%.1f/s p95=%s p99=%s errors=%d pool_waits=%d", strings.Split(version, ",")[0], workers, len(times), total, float64(len(times))/total.Seconds(), times[len(times)*95/100], times[len(times)*99/100], failures.Load(), s.DB.Stats().WaitCount+b.DB.Stats().WaitCount)
	if failures.Load() != 0 {
		t.Fatal(fmt.Sprintf("%d failed reads", failures.Load()))
	}
}

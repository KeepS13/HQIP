package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
)

// This is a Store API load test, not an HTTP, Redis, authentication or VPN
// throughput benchmark. Each subtest owns a fresh random schema and synthetic
// accounts. Never point it at the application database or use its role.
//
// GOMAXPROCS=2 HQIP_RUN_POSTGRES_NORMALIZATION_LOAD=1 \
// HQIP_TEST_POSTGRES_URL='postgres://hqip_validation:...@127.0.0.1/hqip_validation?sslmode=disable' \
// go test ./internal/store -run '^TestPostgresNormalizationMixedLoad$' -count=1 -v -timeout=15m
//
// Default: 32 workers for 5 minutes, then 64 workers for 5 minutes. For a smoke
// run only, set HQIP_NORMALIZATION_LOAD_DURATION=2s. The duration excludes setup
// and the separate concurrent admission/replay correctness checks. No calls are
// cancelled midway through settlement when the measurement interval finishes.
func TestPostgresNormalizationMixedLoad(t *testing.T) {
	if testing.Short() || os.Getenv("HQIP_RUN_POSTGRES_NORMALIZATION_LOAD") != "1" {
		t.Skip("opt-in PostgreSQL Store load test; set HQIP_RUN_POSTGRES_NORMALIZATION_LOAD=1")
	}
	source := os.Getenv("HQIP_TEST_POSTGRES_URL")
	u, err := url.Parse(source)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("load test requires an explicit local 127.0.0.1 PostgreSQL URL")
	}
	config, err := pgstore.Config(source)
	if err != nil || config.Database != "hqip_validation" || config.User != "hqip_validation" {
		t.Fatal("load test requires the dedicated hqip_validation database AND role")
	}
	duration := 5 * time.Minute
	if value := os.Getenv("HQIP_NORMALIZATION_LOAD_DURATION"); value != "" {
		duration, err = time.ParseDuration(value)
		if err != nil || duration < time.Second || duration > 10*time.Minute {
			t.Fatal("HQIP_NORMALIZATION_LOAD_DURATION must be between 1s and 10m")
		}
	}
	for _, workers := range []int{32, 64} {
		t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
			runPostgresNormalizationLoad(t, workers, duration)
		})
	}
}

const (
	normalizationSubscriptionRead = iota
	normalizationAccountRead
	normalizationAcquire
	normalizationRenew
	normalizationClose
	normalizationReplay
	normalizationOperationCount
	// 1 ms upper-bound buckets through 10 seconds, plus one overflow bucket.
	// All observations are counted; memory never grows with elapsed time or RPS.
	normalizationHistogramSize = 10002
)

var normalizationOperationNames = [normalizationOperationCount]string{
	"subscription_read", "account_read", "lease_acquire", "lease_renew", "lease_close", "lease_close_replay",
}

type normalizationLoadSubject struct {
	user, subscription, password string
	expectedBytes                atomic.Int64
}

type normalizationLoadMetric struct {
	calls, failures, rejected, nanos, maximum atomic.Uint64
	buckets                                   [normalizationHistogramSize]atomic.Uint64
}

type normalizationLoadMetrics struct {
	operations [normalizationOperationCount]normalizationLoadMetric
	cycles     atomic.Uint64
	samples    chan string // At most eight diagnostic errors; no unbounded queue.
}

func (m *normalizationLoadMetrics) record(kind int, start time.Time, err error, rejected bool) bool {
	nanos := uint64(time.Since(start))
	v := &m.operations[kind]
	v.calls.Add(1)
	v.nanos.Add(nanos)
	for old := v.maximum.Load(); nanos > old && !v.maximum.CompareAndSwap(old, nanos); old = v.maximum.Load() {
	}
	bucket := (nanos + uint64(time.Millisecond) - 1) / uint64(time.Millisecond)
	if bucket >= normalizationHistogramSize {
		bucket = normalizationHistogramSize - 1
	}
	v.buckets[bucket].Add(1)
	if err == nil {
		return true
	}
	if rejected {
		v.rejected.Add(1)
		return true
	}
	v.failures.Add(1)
	message := normalizationOperationNames[kind] + ": " + err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	select {
	case m.samples <- message:
	default:
	}
	return false
}

func normalizationPercentile(buckets []uint64, count uint64, percentile uint64) string {
	if count == 0 {
		return "n/a"
	}
	target, seen := (count*percentile+99)/100, uint64(0)
	for i, n := range buckets {
		seen += n
		if seen >= target {
			if i == normalizationHistogramSize-1 {
				return ">10s"
			}
			return (time.Duration(i) * time.Millisecond).String()
		}
	}
	return "n/a"
}

func normalizationSubscriptionInvariant(e *model.UserSubscription) error {
	if e == nil || e.QuotaBytes == nil {
		return errors.New("missing bounded subscription")
	}
	if e.UsedBytes < 0 || e.ReservedBytes < 0 || e.UsedBytes > *e.QuotaBytes || e.ReservedBytes > *e.QuotaBytes-e.UsedBytes {
		return errors.New("subscription quota oversold or negative ledger")
	}
	if e.Occupied < 0 || e.Occupied > e.MaxConnections {
		return errors.New("subscription connection slots oversold")
	}
	return nil
}

// Exercise real contention at both slot and quota boundaries before measuring.
// The replay race submits the identical final receipt from every worker through
// two independent connection pools; its usage must be charged exactly once.
func normalizationAdmissionAndReplay(t *testing.T, pools [2]*Store, n model.Node, subject *normalizationLoadSubject, workers, admitted int) {
	t.Helper()
	type result struct {
		lease model.ConnectionLease
		err   error
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	for i := 0; i < workers; i++ {
		ready.Add(1)
		done.Add(1)
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			lease, err := pools[i%2].AcquireLease(n.ID, "load-guard-"+NewID(), subject.password)
			results <- result{lease, err}
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(results)
	leases := make([]model.ConnectionLease, 0, workers)
	for r := range results {
		if r.err == nil {
			leases = append(leases, r.lease)
		} else if !errors.Is(r.err, ErrSlots) && !errors.Is(r.err, ErrEntitlement) {
			t.Fatal("unexpected contention result:", r.err)
		}
	}
	if len(leases) != admitted {
		t.Fatalf("admission boundary: got %d leases, want %d", len(leases), admitted)
	}
	e, err := pools[1].UserSubscription(subject.user, subject.subscription)
	if err != nil {
		t.Fatal(err)
	}
	if err = normalizationSubscriptionInvariant(e); err != nil || e.Occupied != admitted {
		t.Fatalf("admission invariant failed: %v, occupied=%d", err, e.Occupied)
	}
	for _, lease := range leases {
		results := make(chan error, workers)
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			ready.Add(1)
			done.Add(1)
			go func(i int) {
				defer done.Done()
				ready.Done()
				<-start
				v, err := pools[i%2].RenewLease(n.ID, lease.ID, model.LeaseUsage{UploadBytes: 17, DownloadBytes: 31, Closed: true, AppliedRateVersion: lease.RateVersion})
				if err == nil && (!v.Closed || v.UploadBytes != 17 || v.DownloadBytes != 31) {
					err = errors.New("replayed final receipt changed")
				}
				results <- err
			}(i)
		}
		ready.Wait()
		close(start)
		done.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal("concurrent settlement replay:", err)
			}
		}
	}
	e, err = pools[1].UserSubscription(subject.user, subject.subscription)
	if err != nil || e.UsedBytes != int64(admitted*48) || e.ReservedBytes != 0 || e.Occupied != 0 {
		t.Fatalf("settlement replay double-charged or left reservations: %v", err)
	}
}

func normalizationLeaseCycle(pools [2]*Store, pool int, n model.Node, subject *normalizationLoadSubject, metrics *normalizationLoadMetrics) bool {
	start := time.Now()
	lease, err := pools[pool].AcquireLease(n.ID, "normalization-"+NewID(), subject.password)
	if !metrics.record(normalizationAcquire, start, err, errors.Is(err, ErrSlots)) {
		return false
	}
	if err != nil { // Expected slot pressure is reported separately from errors.
		return true
	}
	start = time.Now()
	lease, err = pools[pool].RenewLease(n.ID, lease.ID, model.LeaseUsage{UploadBytes: 256, DownloadBytes: 512, AppliedRateVersion: lease.RateVersion})
	if !metrics.record(normalizationRenew, start, err, false) {
		return false
	}
	subject.expectedBytes.Add(768)
	start = time.Now()
	usage := model.LeaseUsage{UploadBytes: 512, DownloadBytes: 1024, Closed: true, AppliedRateVersion: lease.RateVersion}
	closed, err := pools[pool].RenewLease(n.ID, lease.ID, usage)
	if err == nil && (!closed.Closed || closed.UploadBytes != usage.UploadBytes || closed.DownloadBytes != usage.DownloadBytes) {
		err = errors.New("final receipt does not match reported usage")
	}
	if !metrics.record(normalizationClose, start, err, false) {
		return false
	}
	subject.expectedBytes.Add(768)
	start = time.Now()
	replay, err := pools[1-pool].RenewLease(n.ID, lease.ID, usage)
	if err == nil && (!replay.Closed || replay.ID != closed.ID || replay.UploadBytes != closed.UploadBytes || replay.DownloadBytes != closed.DownloadBytes) {
		err = errors.New("cross-pool final receipt replay changed")
	}
	if !metrics.record(normalizationReplay, start, err, false) {
		return false
	}
	metrics.cycles.Add(1)
	return true
}

func normalizationPoolDelta(before, after sql.DBStats) map[string]any {
	return map[string]any{
		"max_connections": after.MaxOpenConnections, "open_at_end": after.OpenConnections,
		"in_use_at_end": after.InUse, "wait_count": after.WaitCount - before.WaitCount,
		"wait_duration_ms": float64(after.WaitDuration-before.WaitDuration) / float64(time.Millisecond),
	}
}

func runPostgresNormalizationLoad(t *testing.T, workers int, duration time.Duration) {
	t.Helper()
	quota := int64(1 << 30)
	s, now, seed, template, n, password := portalFixture(t, 2, &quota)
	peer, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	pools := [2]*Store{s, peer}
	fixtureStart, monotonicStart := *now, time.Now()
	s.Now = func() time.Time { return fixtureStart.Add(time.Since(monotonicStart)) }
	peer.Now = s.Now
	var database, role, schema, version string
	if err = s.DB.QueryRow("SELECT current_database(),current_user,current_schema(),version()").Scan(&database, &role, &schema, &version); err != nil {
		t.Fatal(err)
	}
	if database != "hqip_validation" || role != "hqip_validation" || !strings.HasPrefix(schema, "test_") {
		t.Fatal("load test did not receive its dedicated database, role and random fixture schema")
	}
	guard := &normalizationLoadSubject{user: seed.ID, subscription: seed.Subscriptions[0].ID, password: password}
	normalizationAdmissionAndReplay(t, pools, n, guard, workers, 2)
	tinyQuota := 2 * model.MinimumSubscriptionLeaseBytes
	tiny, err := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "load-budget-guard", Enabled: true, ValidityDays: 1, NodeIDs: []string{n.ID}, DefaultNodeID: n.ID, MaxConnections: workers, QuotaBytes: &tinyQuota}, 0)
	if err != nil {
		t.Fatal(err)
	}
	small := grantAnother(t, s, seed.ID, tiny, "normalization-budget-guard")
	guard = &normalizationLoadSubject{user: seed.ID, subscription: small.ID, password: subscriptionPassword(t, s, seed.ID, small.ID, n.ID)}
	normalizationAdmissionAndReplay(t, pools, n, guard, workers, 1)

	const accounts = 16
	subjects := make([]*normalizationLoadSubject, accounts)
	for i := range subjects {
		user, err := s.RegisterPortal(fmt.Sprintf("normalization-%02d@example.test", i), "synthetic-load-password-only")
		if err != nil {
			t.Fatal(err)
		}
		sub := grantAnother(t, s, user.ID, template, "normalization-initial-subscription")
		subjects[i] = &normalizationLoadSubject{user: user.ID, subscription: sub.ID, password: subscriptionPassword(t, s, user.ID, sub.ID, n.ID)}
	}
	before := [2]sql.DBStats{s.DB.Stats(), peer.DB.Stats()}
	metrics := &normalizationLoadMetrics{samples: make(chan string, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startSignal := make(chan struct{})
	var ready, done sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		ready.Add(1)
		done.Add(1)
		go func(worker int) {
			defer done.Done()
			ready.Done()
			<-startSignal
			pool := worker % len(pools)
			for iteration := 0; ctx.Err() == nil; iteration++ {
				subject := subjects[(worker+iteration)%len(subjects)]
				if iteration%10 >= 6 {
					if !normalizationLeaseCycle(pools, pool, n, subject, metrics) {
						cancel()
					}
					continue
				}
				at := time.Now()
				kind := normalizationSubscriptionRead
				var err error
				if iteration%10 < 3 {
					var sub *model.UserSubscription
					sub, err = pools[pool].UserSubscription(subject.user, subject.subscription)
					if err == nil {
						err = normalizationSubscriptionInvariant(sub)
					}
				} else {
					kind = normalizationAccountRead
					var user model.PortalUser
					user, err = pools[pool].PortalUser(subject.user)
					if err == nil && (user.ID != subject.user || len(user.Subscriptions) != 1 || user.Subscriptions[0].ID != subject.subscription) {
						err = errors.New("account read crossed subscription identity")
					}
					if err == nil {
						err = normalizationSubscriptionInvariant(&user.Subscriptions[0])
					}
				}
				if !metrics.record(kind, at, err, false) {
					cancel()
				}
			}
		}(worker)
	}
	ready.Wait()
	started := time.Now()
	timer := time.AfterFunc(duration, cancel)
	close(startSignal)
	done.Wait()
	timer.Stop()
	elapsed := time.Since(started)
	after := [2]sql.DBStats{s.DB.Stats(), peer.DB.Stats()}
	close(metrics.samples)
	samples := make([]string, 0, cap(metrics.samples))
	for sample := range metrics.samples {
		samples = append(samples, sample)
	}

	// Quiescent reads compare committed bytes with the independently accumulated
	// successful cumulative reports. Replayed reports never add expected bytes.
	var ledgerErrors []string
	for i, subject := range subjects {
		e, err := peer.UserSubscription(subject.user, subject.subscription)
		if err == nil {
			err = normalizationSubscriptionInvariant(e)
		}
		if err == nil && (e.UsedBytes != subject.expectedBytes.Load() || e.ReservedBytes != 0 || e.Occupied != 0) {
			err = errors.New("replay charged twice or lease reservations were not released")
		}
		if err != nil {
			ledgerErrors = append(ledgerErrors, fmt.Sprintf("subject_%d: %s", i, err))
		}
	}
	counts := make([]uint64, normalizationHistogramSize)
	operations := make(map[string]any, normalizationOperationCount)
	var total, failures, rejected uint64
	for kind := range metrics.operations {
		v := &metrics.operations[kind]
		calls := v.calls.Load()
		bucketCounts := make([]uint64, normalizationHistogramSize)
		for i := range bucketCounts {
			bucketCounts[i] = v.buckets[i].Load()
			counts[i] += bucketCounts[i]
		}
		mean := float64(0)
		if calls != 0 {
			mean = float64(v.nanos.Load()) / float64(calls) / float64(time.Millisecond)
		}
		operations[normalizationOperationNames[kind]] = map[string]any{
			"calls": calls, "errors": v.failures.Load(), "expected_rejections": v.rejected.Load(),
			"p95": normalizationPercentile(bucketCounts, calls, 95), "p99": normalizationPercentile(bucketCounts, calls, 99),
			"mean_ms": mean, "max_ms": float64(v.maximum.Load()) / float64(time.Millisecond),
		}
		total += calls
		failures += v.failures.Load()
		rejected += v.rejected.Load()
	}
	result := map[string]any{
		"scope":    "PostgreSQL Store API only; excludes HTTP/Redis/authentication/VPN",
		"database": database, "role": role, "schema": schema, "database_version": strings.Split(version, ",")[0],
		"workers": workers, "accounts": accounts, "pools": 2, "gomaxprocs": runtime.GOMAXPROCS(0),
		"configured_seconds": duration.Seconds(), "elapsed_seconds": elapsed.Seconds(),
		"mix":   "60% read iterations; 40% acquire/renew/close/replay cycles; slot rejection is expected",
		"calls": total, "calls_per_second": float64(total) / elapsed.Seconds(),
		"successful_calls_per_second": float64(total-failures-rejected) / elapsed.Seconds(),
		"completed_lease_cycles":      metrics.cycles.Load(), "errors": failures, "expected_rejections": rejected,
		"p95": normalizationPercentile(counts, total, 95), "p99": normalizationPercentile(counts, total, 99),
		"latency_measurement": "all Store calls; bounded atomic histogram; 1ms upper bounds, >10s overflow",
		"operations":          operations, "pool_stats": []any{normalizationPoolDelta(before[0], after[0]), normalizationPoolDelta(before[1], after[1])},
		"error_samples": samples, "ledger_errors": ledgerErrors,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("normalization_load_json=%s", encoded)
	if failures != 0 || len(ledgerErrors) != 0 || metrics.cycles.Load() == 0 {
		t.Fatal("mixed load failed: require zero unexpected errors, exact final accounting and completed lease cycles")
	}
}

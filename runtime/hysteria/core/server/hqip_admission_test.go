package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const admissionTestSecret = "test-admission-secret-long-enough"

func testLease(t *testing.T, budget, rate int64, expiry time.Duration) (*connectionLease, *atomic.Int32) {
	t.Helper()
	closed := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(LeaseGrant{LeaseID: "lease-1", UserID: "user-1", AccountingID: "device-1", BudgetBytes: budget, BandwidthBytesPerSecond: rate, ExpiresAt: time.Now().Add(expiry), Revision: 1})
	}))
	t.Cleanup(server.Close)
	a, err := NewAdmission(AdmissionConfig{URL: server.URL, Secret: admissionTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	l, _, err := a.open(context.Background(), randomConnectionID(), "random-password-012345", "198.51.100.1", func() { closed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.finish() })
	return l, closed
}

func TestHQIPBudgetPreventsConcurrentOverspend(t *testing.T) {
	l, closed := testLease(t, 1000, 0, 30*time.Second)
	var wg sync.WaitGroup
	var passed atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.consume(true, 100) == nil {
				passed.Add(100)
			}
		}()
	}
	wg.Wait()
	if passed.Load() > 1000 {
		t.Fatalf("overspent %d", passed.Load())
	}
	l.mu.Lock()
	used := l.usedUp + l.usedDown
	l.mu.Unlock()
	if used > 1000 || closed.Load() != 1 {
		t.Fatalf("usage=%d closed=%d", used, closed.Load())
	}
}

func TestHQIPIndependentDirectionsShareBudget(t *testing.T) {
	l, _ := testLease(t, 1<<20, 10000, 30*time.Second)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(upload bool) {
			defer wg.Done()
			if err := l.consume(upload, 5000); err != nil {
				t.Error(err)
			}
		}(i == 0)
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed < 475*time.Millisecond || elapsed > 950*time.Millisecond {
		t.Fatalf("directions must independently run at granted rate: %s", elapsed)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.usedUp != 5000 || l.usedDown != 5000 {
		t.Fatal("direction counters incorrect")
	}
}

func TestHQIPExpiryDisconnectsIdleConnectionAndCannotResume(t *testing.T) {
	l, closed := testLease(t, 1<<20, 0, 150*time.Millisecond)
	select {
	case <-l.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle connection survived expiry")
	}
	// fail cancels the context before invoking the close callback, while holding
	// mu. Context cancellation alone does not mean that callback has completed.
	l.mu.Lock()
	leaseClosed, closeCalls, g := l.closed, closed.Load(), l.grant
	l.mu.Unlock()
	if !leaseClosed || closeCalls != 1 {
		t.Fatalf("not an exact connection close: leaseClosed=%t calls=%d", leaseClosed, closeCalls)
	}
	if l.consume(true, 1) == nil {
		t.Fatal("expired connection forwarded payload")
	}
	g.ExpiresAt = time.Now().Add(time.Hour)
	if l.acceptGrant(g, time.Now(), false) == nil {
		t.Fatal("closed lease revived")
	}
}

func TestHQIPLeaseCeilingAndCountersCannotReset(t *testing.T) {
	l, _ := testLease(t, 1<<20, 0, time.Hour)
	l.mu.Lock()
	remaining := time.Until(l.deadline)
	g := l.grant
	l.mu.Unlock()
	if remaining > 120*time.Second {
		t.Fatal("authorization exceeds 120 second local ceiling")
	}
	if err := l.consume(true, 10); err != nil {
		t.Fatal(err)
	}
	g.BudgetBytes--
	if l.acceptGrant(g, time.Now(), false) == nil {
		t.Fatal("budget reset accepted")
	}
	g = l.grant
	g.LeaseID = "different"
	if l.acceptGrant(g, time.Now(), false) == nil {
		t.Fatal("lease identity replaced")
	}
}

func TestHQIPRatePendingBlocksUntilNewVersionAndAcknowledges(t *testing.T) {
	l, _ := testLease(t, 1<<20, 0, 30*time.Second)
	l.mu.Lock()
	g := l.grant
	l.mu.Unlock()
	g.RatePending = true
	g.RateVersion = 1
	if err := l.acceptGrant(g, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.consume(true, 100) }()
	select {
	case err := <-done:
		t.Fatal("pending rate forwarded bytes", err)
	case <-time.After(80 * time.Millisecond):
	}
	g.RatePending = false
	if l.acceptGrant(g, time.Now(), false) == nil {
		t.Fatal("same-version pending change accepted")
	}
	g.RateVersion = 2
	g.BandwidthBytesPerSecond = 10000
	if err := l.acceptGrant(g, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("new rate did not resume")
	}
	var ack atomic.Int64
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u leaseUsage
		_ = json.NewDecoder(r.Body).Decode(&u)
		ack.Store(u.AppliedRateVersion)
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer local.Close()
	a, err := NewAdmission(AdmissionConfig{URL: local.URL, Secret: admissionTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	l.admission = a
	if err = l.renew(); err != nil {
		t.Fatal(err)
	}
	if ack.Load() != 2 {
		t.Fatal("applied rate was not acknowledged")
	}
}

func TestHQIPPendingExpiresWithoutForwarding(t *testing.T) {
	l, _ := testLease(t, 1<<20, 0, 150*time.Millisecond)
	l.mu.Lock()
	g := l.grant
	l.mu.Unlock()
	g.RatePending = true
	g.RateVersion = 1
	if err := l.acceptGrant(g, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if err := l.consume(true, 1); err == nil {
		t.Fatal("pending grant forwarded payload")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.usedUp != 0 {
		t.Fatal("paused bytes charged")
	}
}

func TestHQIPPendingResumeRechunksAtSmallNewShare(t *testing.T) {
	l, _ := testLease(t, 1<<20, 0, 30*time.Second)
	l.mu.Lock()
	g := l.grant
	l.mu.Unlock()
	g.RatePending = true
	g.RateVersion = 1
	if err := l.acceptGrant(g, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.consume(true, 40) }()
	time.Sleep(20 * time.Millisecond)
	g.RatePending = false
	g.RateVersion = 2
	g.BandwidthBytesPerSecond = 100
	if err := l.acceptGrant(g, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("small released share stalled")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.usedUp != 40 {
		t.Fatal("rechunk lost or duplicated bytes")
	}
}

func TestHQIPLegacyRenewalDenialClosesExactConnection(t *testing.T) {
	var deny atomic.Bool
	var closed atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["legacyRenewal"] != "" && deny.Load() {
			http.Error(w, "revoked", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(LeaseGrant{Legacy: true, AccountingID: "old-device"})
	}))
	defer local.Close()
	a, err := NewAdmission(AdmissionConfig{URL: local.URL, Secret: admissionTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	l, _, err := a.open(context.Background(), randomConnectionID(), "old-device-password", "198.51.100.1", func() { closed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer l.finish()
	if err = l.consume(true, 1); err != nil {
		t.Fatal(err)
	}
	deny.Store(true)
	if err = l.renew(); err == nil || closed.Load() != 1 {
		t.Fatal("legacy live connection survived activation")
	}
	if l.consume(false, 1) == nil {
		t.Fatal("legacy bytes passed after revocation")
	}
}

package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func adminTestRedis(t *testing.T) (*miniredis.Miniredis, *Redis, *Redis) {
	t.Helper()
	db := miniredis.RunT(t)
	db.SetTime(time.Unix(1800000000, 0))
	a, err := Open(context.Background(), "redis://"+db.Addr(), "test-admin")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(context.Background(), "redis://"+db.Addr(), "test-admin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return db, a, b
}

func TestAdminSourceNormalizesAndGroupsIPv6(t *testing.T) {
	for input, expected := range map[string]string{
		"198.51.100.7": "198.51.100.7", "::ffff:198.51.100.7": "198.51.100.7",
		"2001:db8:abcd:1234::1": "2001:db8:abcd:1234::/64", "2001:0DB8:abcd:1234:abcd::12": "2001:db8:abcd:1234::/64",
	} {
		got, err := AdminSource(input)
		if err != nil || got != expected {
			t.Fatalf("%q: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"", "hostname", "1.2.3.4,5.6.7.8", "fe80::1%eth0"} {
		if _, err := AdminSource(input); err == nil {
			t.Fatalf("accepted invalid source %q", input)
		}
	}
}

func TestAdminSharedBudgetsAreAtomic(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		limit   int64
		address func(int) string
	}{
		{"source", 10, func(int) string { return "198.51.100.1" }},
		{"global", 30, func(i int) string { return fmt.Sprintf("198.51.100.%d", i+1) }},
		{"ipv6-subnet", 10, func(i int) string { return fmt.Sprintf("2001:db8:1:2::%x", i+1) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, a, b := adminTestRedis(t)
			var allowed atomic.Int64
			var wg sync.WaitGroup
			for i := 0; i < 60; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					client := a
					if i%2 == 1 {
						client = b
					}
					decision, err := client.AdminAdmit(context.Background(), scenario.address(i))
					if err != nil {
						t.Error(err)
						return
					}
					if decision.Allowed {
						allowed.Add(1)
					}
				}(i)
			}
			wg.Wait()
			if allowed.Load() != scenario.limit {
				t.Fatalf("allowed %d, want %d", allowed.Load(), scenario.limit)
			}
		})
	}
}

func TestAdminProgressiveBansExpireWithoutDeniedRequestExtension(t *testing.T) {
	clock := time.Unix(1800000000, 0)
	db, a, b := adminTestRedis(t)
	ip := "198.51.100.1"
	ctx := context.Background()
	for n := 1; n <= 20; n++ {
		// Requests also obey the 10/15m source window. Advancing 16m between
		// actual failures keeps that budget clear while retaining 24h failures.
		clock = clock.Add(16 * time.Minute)
		db.SetTime(clock)
		admit, err := b.AdminAdmit(ctx, ip)
		if err != nil {
			t.Fatal(err)
		}
		if !admit.Allowed && admit.BanLevel == 3 {
			clock = clock.Add(time.Hour)
			db.SetTime(clock)
			admit, err = b.AdminAdmit(ctx, ip)
		}
		if err != nil || !admit.Allowed {
			t.Fatalf("admission %d: %+v %v", n, admit, err)
		}
		decision, err := a.AdminFailure(ctx, ip)
		if err != nil || decision.Failures != n {
			t.Fatalf("failure %d: %+v %v", n, decision, err)
		}
		wantLevel, duration := adminBan(n)
		if decision.BanLevel != wantLevel || decision.BanStarted != (wantLevel > 0) {
			t.Fatalf("failure %d: %+v", n, decision)
		}
		if wantLevel == 0 {
			continue
		}
		clock = clock.Add(duration - time.Second)
		db.SetTime(clock)
		blocked, err := b.AdminAdmit(ctx, ip)
		if err != nil || blocked.Allowed || blocked.Reason != "temporary_ban" || blocked.BanLevel != wantLevel {
			t.Fatalf("ban %d: %+v %v", n, blocked, err)
		}
		clock = clock.Add(time.Second)
		db.SetTime(clock)
		open, err := b.AdminAdmit(ctx, ip)
		if err != nil || !open.Allowed {
			t.Fatalf("ban extended %d: %+v %v", n, open, err)
		}
	}
}

func TestAdminSuccessClearsOnlyFailuresAndPreservesBudgets(t *testing.T) {
	_, a, _ := adminTestRedis(t)
	ctx := context.Background()
	ip := "198.51.100.1"
	for i := 0; i < 10; i++ {
		decision, err := a.AdminAdmit(ctx, ip)
		if err != nil || !decision.Allowed {
			t.Fatal(decision, err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := a.AdminFailure(ctx, ip); err != nil {
			t.Fatal(err)
		}
		if _, err := a.AdminFailure(ctx, "198.51.100.2"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.AdminSuccess(ctx, ip); err != nil {
		t.Fatal(err)
	}
	decision, err := a.AdminAdmit(ctx, ip)
	if err != nil || decision.Reason != "source_rate" {
		t.Fatal("success erased request budget", decision, err)
	}
	decision, err = a.AdminFailure(ctx, ip)
	if err != nil || decision.Failures != 1 || decision.BanLevel != 0 {
		t.Fatal("failures not cleared", decision, err)
	}
	decision, err = a.AdminAdmit(ctx, "198.51.100.2")
	if err != nil || decision.Reason != "temporary_ban" {
		t.Fatal("other source cleared", decision, err)
	}
}

func TestAdminBlockedRequestsDoNotConsumeGlobalBudget(t *testing.T) {
	_, a, _ := adminTestRedis(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := a.AdminFailure(ctx, "198.51.100.1"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		decision, err := a.AdminAdmit(ctx, "198.51.100.1")
		if err != nil || decision.Allowed {
			t.Fatal(decision, err)
		}
	}
	for i := 0; i < 30; i++ {
		decision, err := a.AdminAdmit(ctx, fmt.Sprintf("198.51.100.%d", i+2))
		if err != nil || !decision.Allowed {
			t.Fatalf("global budget consumed at %d: %+v %v", i, decision, err)
		}
	}
}

func TestAdminFailureStateExpiresAndRedisFailureCloses(t *testing.T) {
	db, a, _ := adminTestRedis(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := a.AdminFailure(ctx, "198.51.100.1"); err != nil {
			t.Fatal(err)
		}
	}
	db.SetTime(time.Unix(1800000000, 0).Add(24 * time.Hour))
	decision, err := a.AdminFailure(ctx, "198.51.100.1")
	if err != nil || decision.Failures != 1 || decision.BanLevel != 0 {
		t.Fatal(decision, err)
	}
	db.Close()
	if decision, err = a.AdminAdmit(ctx, "198.51.100.1"); err == nil || decision.Allowed {
		t.Fatal("Redis failure admitted", decision, err)
	}
	if _, err := a.AdminFailure(ctx, "198.51.100.1"); err == nil {
		t.Fatal("Redis failure ignored")
	}
	if err := a.AdminSuccess(ctx, "198.51.100.1"); err == nil {
		t.Fatal("Redis failure ignored")
	}
}

func TestAdminMemoryUsesSameLimitsAndIPv6Identity(t *testing.T) {
	now := time.Unix(1800000000, 0)
	m := NewMemoryAdminProtection(func() time.Time { return now })
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := m.AdminFailure(ctx, "2001:db8:1:2::1"); err != nil {
			t.Fatal(err)
		}
	}
	if d, err := m.AdminAdmit(ctx, "2001:db8:1:2::99"); err != nil || d.BanLevel != 1 {
		t.Fatal(d, err)
	}
	now = now.Add(time.Minute)
	for i := 0; i < 10; i++ {
		if d, err := m.AdminAdmit(ctx, "2001:db8:1:2::1"); err != nil || !d.Allowed {
			t.Fatal(d, err)
		}
	}
	if err := m.AdminSuccess(ctx, "2001:db8:1:2::1"); err != nil {
		t.Fatal(err)
	}
	if d, err := m.AdminAdmit(ctx, "2001:db8:1:2::99"); err != nil || d.Reason != "source_rate" {
		t.Fatal(d, err)
	}
	for i := 0; i < 20; i++ {
		if d, err := m.AdminAdmit(ctx, fmt.Sprintf("198.51.100.%d", i+1)); err != nil || !d.Allowed {
			t.Fatal(d, err)
		}
	}
	if d, err := m.AdminAdmit(ctx, "203.0.113.1"); err != nil || d.Reason != "global_rate" {
		t.Fatal(d, err)
	}
}

func TestAdminConcurrentFailuresStartOnlyThresholdBans(t *testing.T) {
	_, a, b := adminTestRedis(t)
	var started atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := a
			if i%2 == 1 {
				client = b
			}
			d, err := client.AdminFailure(context.Background(), "198.51.100.1")
			if err != nil {
				t.Error(err)
				return
			}
			if d.BanStarted {
				started.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if started.Load() != 3 {
		t.Fatal("concurrent failures duplicated or lost threshold transitions", started.Load())
	}
	d, err := a.AdminAdmit(context.Background(), "198.51.100.1")
	if err != nil || d.BanLevel != 3 || d.RetryAfter != 3600 {
		t.Fatal(d, err)
	}
}

func TestAdminSuccessDoesNotResetGlobalRequestBudget(t *testing.T) {
	_, a, _ := adminTestRedis(t)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i+1)
		if d, err := a.AdminAdmit(ctx, ip); err != nil || !d.Allowed {
			t.Fatal(d, err)
		}
		if err := a.AdminSuccess(ctx, ip); err != nil {
			t.Fatal(err)
		}
	}
	if d, err := a.AdminAdmit(ctx, "203.0.113.1"); err != nil || d.Reason != "global_rate" {
		t.Fatal("successful logins reset global budget", d, err)
	}
}

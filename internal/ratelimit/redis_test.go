package ratelimit

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestSharedWindowIsAtomicAcrossInstances(t *testing.T) {
	s := miniredis.RunT(t)
	now := time.Unix(1800000000, 0)
	s.SetTime(now)
	a, err := Open(context.Background(), "redis://"+s.Addr(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(context.Background(), "redis://"+s.Addr(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := a
			if i%2 == 0 {
				c = b
			}
			ok, err := c.Allow(context.Background(), "user@example.com", 10, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("admitted %d, want exactly 10", allowed.Load())
	}
	for _, key := range s.Keys() {
		if strings.Contains(key, "example") {
			t.Fatal("private identifier in Redis key")
		}
		if s.TTL(key) <= 0 {
			t.Fatal("missing expiry")
		}
	}
	s.SetTime(now.Add(time.Minute))
	if ok, err := b.Allow(context.Background(), "user@example.com", 10, time.Minute); !ok || err != nil {
		t.Fatalf("window did not expire: %v %v", ok, err)
	}
	if ok, err := a.Allow(context.Background(), "another-user", 10, time.Minute); !ok || err != nil {
		t.Fatal("keys not isolated")
	}
}

func TestRedisFailureNeverPermitsRequests(t *testing.T) {
	s := miniredis.RunT(t)
	r, err := Open(context.Background(), "redis://"+s.Addr(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if ok, err := r.Allow(ctx, "key", 10, time.Minute); ok || err == nil {
		t.Fatal("failure admitted request")
	}
	for _, url := range []string{"redis://private.example:6379", "rediss://:secret@127.0.0.1:1", "invalid"} {
		_, err := Open(ctx, url, "test")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe connection/error", err)
		}
	}
}

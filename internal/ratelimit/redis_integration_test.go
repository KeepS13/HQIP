package ratelimit

import (
	"context"
	"github.com/KeepS13/HQIP/internal/secure"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRealRedisSharedBudget(t *testing.T) {
	address := os.Getenv("HQIP_TEST_REDIS_URL")
	if address == "" {
		t.Skip("real Redis integration URL not configured")
	}
	u, err := url.Parse(address)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("integration requires local Redis")
	}
	namespace := "hqip-test-" + secure.Token()
	a, err := Open(context.Background(), address, namespace)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(context.Background(), address, namespace)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var count atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := a
			if i%2 == 0 {
				c = b
			}
			allowed, e := c.Allow(context.Background(), "same-user", 10, time.Minute)
			if e != nil {
				t.Error(e)
			}
			if allowed {
				count.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if count.Load() != 10 {
		t.Fatal("shared budget exceeded", count.Load())
	}
	if err = a.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

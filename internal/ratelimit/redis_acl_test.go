package ratelimit

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/secure"
)

func TestRealRedisRestrictedACL(t *testing.T) {
	address := os.Getenv("HQIP_TEST_REDIS_ACL_URL")
	if address == "" {
		t.Skip("generated storage stack not configured")
	}
	u, err := url.Parse(address)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("ACL test requires local disposable Redis")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := Open(ctx, address, "hqip")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	key := secure.Token()
	for i := 0; i < 3; i++ {
		allowed, err := r.Allow(ctx, key, 2, time.Minute)
		if err != nil || allowed != (i < 2) {
			t.Fatal("generated ACL cannot execute limiter", i, allowed, err)
		}
	}
	if err = r.client.Get(ctx, "unrelated:key").Err(); err == nil || !strings.Contains(err.Error(), "NOPERM") {
		t.Fatal("unrelated key access was not denied")
	}
	if err = r.client.ConfigGet(ctx, "maxmemory").Err(); err == nil || !strings.Contains(err.Error(), "NOPERM") {
		t.Fatal("server configuration access was not denied")
	}
}

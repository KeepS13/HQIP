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

func TestRealRedisAdminProtectionOriginalRestrictedACL(t *testing.T) {
	address := os.Getenv("HQIP_TEST_REDIS_ACL_URL")
	if address == "" {
		t.Skip("generated original restricted Redis ACL not configured")
	}
	u, err := url.Parse(address)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("ACL test requires local disposable Redis")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r, err := Open(ctx, address, "hqip")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.prefix += "admin-test:" + secure.Token() + ":"
	ip := "198.51.100.1"
	for i := 0; i < 5; i++ {
		if d, err := r.AdminAdmit(ctx, ip); err != nil || !d.Allowed {
			t.Fatal("original ACL cannot admit", d, err)
		}
		if _, err := r.AdminFailure(ctx, ip); err != nil {
			t.Fatal("original ACL cannot record failure", err)
		}
	}
	if d, err := r.AdminAdmit(ctx, ip); err != nil || d.Reason != "temporary_ban" {
		t.Fatal("original ACL cannot check ban", d, err)
	}
	if err := r.AdminSuccess(ctx, ip); err != nil {
		t.Fatal("original ACL cannot reset failures", err)
	}
	for i := 0; i < 5; i++ {
		if d, err := r.AdminAdmit(ctx, ip); err != nil || !d.Allowed {
			t.Fatal(d, err)
		}
	}
	if d, err := r.AdminAdmit(ctx, ip); err != nil || d.Reason != "source_rate" {
		t.Fatal("success erased original request budget", d, err)
	}
	for _, key := range []string{r.prefix + "unneeded-get", "unrelated:key"} {
		if err := r.client.Get(ctx, key).Err(); err == nil || !strings.Contains(err.Error(), "NOPERM") {
			t.Fatal("unexpected GET ACL expansion", err)
		}
	}
	if err := r.client.ConfigGet(ctx, "maxmemory").Err(); err == nil || !strings.Contains(err.Error(), "NOPERM") {
		t.Fatal("configuration ACL expanded", err)
	}
}

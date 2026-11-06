package store

import (
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestServerExpiryThresholds(t *testing.T) {
	expires := time.Date(2026, 11, 20, 15, 0, 0, 0, Shanghai)
	b := model.ServerBilling{Status: "active", ExpiresAt: &expires}
	for _, tc := range []struct {
		before time.Duration
		stage  int
	}{
		{48*time.Hour + time.Second, 0}, {48 * time.Hour, 1},
		{24*time.Hour + time.Second, 1}, {24 * time.Hour, 2},
		{12 * time.Hour, 3}, {time.Second, 3}, {0, 4}, {-24 * time.Hour, 4},
	} {
		c, ok := serverExpiryCondition("server", "测试服务器", b, expires.Add(-tc.before))
		if ok != (tc.stage > 0) || c.Stage != tc.stage {
			t.Fatalf("before=%v stage=%d ok=%t", tc.before, c.Stage, ok)
		}
		if ok && (c.Continuous || c.Category != "system" || c.DetailPath != "/servers/server") {
			t.Fatalf("expiry must remain an advisory infrastructure condition: %+v", c)
		}
	}
	if _, ok := serverExpiryCondition("server", "server", model.ServerBilling{Status: "unconfigured"}, expires); ok {
		t.Fatal("unset expiry must not be invented")
	}
}

func TestServerCycleQuotaUnknownAndPeriodIdentity(t *testing.T) {
	quota := int64(1000)
	b := model.ServerBilling{Status: "active", CycleID: "cycle-1", CycleUpload: 600, CycleDownload: 300, CycleComplete: false}
	c, ok := notificationBillingQuota("server", "id", "name", "/servers/id", b, &quota)
	if !ok || c.Stage != 80 || c.Unknown || c.Key != "server.cycle:id:cycle-1" {
		t.Fatalf("known lower bound already proves threshold: %+v", c)
	}
	b.CycleDownload = 0
	c, ok = notificationBillingQuota("server", "id", "name", "/servers/id", b, &quota)
	if !ok || !c.Unknown {
		t.Fatal("incomplete lower total must not resolve an existing warning")
	}
	b.CycleComplete = true
	if _, ok = notificationBillingQuota("server", "id", "name", "/servers/id", b, &quota); ok {
		t.Fatal("complete subtotal below threshold should end the warning")
	}
	if _, ok = notificationBillingQuota("server", "id", "name", "/servers/id", b, nil); ok {
		t.Fatal("unlimited quota has no warning")
	}
}

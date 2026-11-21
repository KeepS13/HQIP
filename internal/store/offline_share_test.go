package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

// Heartbeats without consumption must not progressively seize other slots'
// budgets. Exercise both legacy raw bytes and protocol-weighted reservations.
func TestOfflineHeartbeatPreservesOtherSlots(t *testing.T) {
	for _, protocol := range []string{"", "hysteria2", "vless"} {
		t.Run(fmt.Sprint("protocol=", protocol), func(t *testing.T) {
			quota := int64(1_000_000_000_000)
			s, now, n, password := offlineFixture(t, 3, &quota)
			if protocol != "" {
				enableBilling(t, s, 150, 150)
			}
			acquire := func(ch, proto string) model.OfflineGrant {
				var g model.OfflineGrant
				var err error
				if proto == "" {
					g, err = s.AcquireOfflineGrant(n.ID, strings.Repeat(ch, 64), password)
				} else {
					g, err = s.AcquireProtocolOfflineGrant(n.ID, strings.Repeat(ch, 64), password, proto)
				}
				if err != nil {
					t.Fatal("admission", err)
				}
				return g
			}
			g := acquire("a", protocol)
			initial := g.NormalBudgetBytes
			for version := int64(1); version <= 100; version++ {
				u := model.OfflineUsage{Version: version, Active: true, AppliedRateVersion: g.RateVersion}
				ack, err := s.ReportOfflineGrant(n.ID, g.ID, u)
				if err != nil || ack.NormalBudgetBytes != initial {
					t.Fatalf("heartbeat %d grew reservation: %d -> %d: %v", version, initial, ack.NormalBudgetBytes, err)
				}
				g = ack
			}
			// Consumption replenishes only the missing share, in billed bytes.
			u := model.OfflineUsage{Version: 101, DownloadBytes: 300, Active: true, AppliedRateVersion: g.RateVersion}
			used := int64(300)
			if g.Billing != nil {
				used = 450
				u.Billing = []billing.Segment{{Version: g.Billing.Version, Download: 300, AppliedAt: *now}}
			}
			ack, err := s.ReportOfflineGrant(n.ID, g.ID, u)
			want := used + (quota-used)/3
			if err != nil || ack.NormalBudgetBytes != want {
				t.Fatalf("replenishment = %d, want %d: %v", ack.NormalBudgetBytes, want, err)
			}
			if _, err = s.ReportOfflineGrant(n.ID, g.ID, u); err != nil {
				t.Fatal("idempotent replay", err)
			}
			second := acquire("b", "hysteria2")
			third := acquire("c", "vless")
			var reserved, actual int64
			if err = s.DB.QueryRow("SELECT reserved,upload+download FROM portal_usage WHERE subscription_id=?", g.SubscriptionID).Scan(&reserved, &actual); err != nil {
				t.Fatal(err)
			}
			if reserved+actual != quota || actual != used || second.NormalBudgetBytes == 0 || third.NormalBudgetBytes == 0 {
				t.Fatalf("quota not conserved: reserved=%d used=%d", reserved, actual)
			}
		})
	}
}

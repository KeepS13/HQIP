package store

import (
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func TestRetiredLegacyKeepsUncertainAllocationAndAcceptsLateFinal(t *testing.T) {
	for _, protocol := range []string{"", "hysteria2"} {
		t.Run(protocol, func(t *testing.T) {
			quota := int64(200_000_000)
			s, now, node, password := offlineFixture(t, 1, &quota)
			var g model.OfflineGrant
			var err error
			if protocol == "" {
				g, err = s.AcquireOfflineGrant(node.ID, strings.Repeat("a", 64), password)
			} else {
				enableBilling(t, s, 150, 150)
				g, err = s.AcquireProtocolOfflineGrant(node.ID, strings.Repeat("a", 64), password, protocol)
			}
			if err != nil {
				t.Fatal(err)
			}
			units := int64(1_000_000)
			u := model.OfflineUsage{Version: 1, DownloadBytes: 1000, RetainedNormalUnits: &units}
			if g.Billing != nil {
				u.Billing = []billing.Segment{{Version: g.Billing.Version, Download: 1000, AppliedAt: *now}}
			}
			ack, err := s.ReportOfflineGrant(node.ID, g.ID, u)
			if err != nil || !ack.Revoked || ack.Closed {
				t.Fatal("retirement lost uncertain final", ack, err)
			}
			replay, err := s.ReportOfflineGrant(node.ID, g.ID, u)
			if err != nil || replay.ReportVersion != ack.ReportVersion {
				t.Fatal("replay", err)
			}
			e, _ := s.UserSubscription(g.UserID, g.SubscriptionID)
			used := int64(1000)
			if g.Billing != nil {
				used = 1500
			}
			if e.UsedBytes != used || e.ReservedBytes != 10_000-used {
				t.Fatal("unissued capacity not returned or uncertain bytes lost", e)
			}
			if _, err = s.legacyNodeQuotaGrant(node.ID, strings.Repeat("b", 64), password, "hysteria2"); err != nil {
				t.Fatal("retired parent still blocks new slot", err)
			}
			u.Version = 2
			u.Closed = true
			u.DownloadBytes = 2000
			u.RetainedNormalUnits = nil
			if g.Billing != nil {
				u.Billing[0].Download = 2000
			}
			if _, err = s.ReportOfflineGrant(node.ID, g.ID, u); err != nil {
				t.Fatal("late final rejected", err)
			}
			e, _ = s.UserSubscription(g.UserID, g.SubscriptionID)
			if e.UsedBytes != used*2 || e.ReservedBytes != 0 {
				t.Fatal("late final was repriced or lost", e)
			}
		})
	}
}

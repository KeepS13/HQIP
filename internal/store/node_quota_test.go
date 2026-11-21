package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func TestNodeQuotaOfflineOverspendReconcilesWithoutLosingUsage(t *testing.T) {
	quota := int64(200_000)
	s, now, first, password := offlineFixture(t, 3, &quota)
	var user, subscription string
	if err := s.DB.QueryRow("SELECT user_id,subscription_id FROM portal_credentials WHERE node_id=?", first.ID).Scan(&user, &subscription); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", now.Add(7*24*time.Hour).Unix(), subscription); err != nil {
		t.Fatal(err)
	}
	second := newNode(t, s, "node-quota-second")
	if err := s.SetStatus(second.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE subscription_allocations SET node_ids=? WHERE subscription_id=?", jsonString([]string{first.ID, second.ID}), subscription); err != nil {
		t.Fatal(err)
	}
	p2, err := s.PortalCredential(user, subscription, second.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetOfflineAdmissionEnabled(second.ID, true); err != nil {
		t.Fatal(err)
	}
	enableBilling(t, s, 150, 150)
	var grants []model.OfflineGrant
	for i, entry := range []struct{ node, password, protocol string }{{first.ID, password, "hysteria2"}, {second.ID, p2, "vless"}} {
		g, err := s.AcquireNodeQuotaGrant(entry.node, strings.Repeat(string(rune('a'+i)), 64), entry.password, entry.protocol)
		if err != nil {
			t.Fatal(err)
		}
		if g.NodeQuota == nil || *g.NodeQuota.QuotaUnits != quota*100 || g.NodeQuota.UsedUnits != 0 || g.NodeQuota.NodeUsedUnits != 0 || g.EmergencyBudgetBytes != 0 || !g.OfflineUntil.After(now.Add(24*time.Hour)) {
			t.Fatalf("wrong full-node snapshot: %+v", g)
		}
		grants = append(grants, g)
	}
	var reserved int64
	if err = s.DB.QueryRow("SELECT reserved FROM portal_usage WHERE subscription_id=?", subscription).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatal("node quota reserved central bytes", reserved, err)
	}
	// Each disconnected node spends 150 KB of its last-known 200 KB balance.
	// Recovery accepts the actual 300 KB total and revokes, even above quota.
	for i, g := range grants {
		u := model.OfflineUsage{Version: 1, DownloadBytes: 100_000, Active: true, AppliedRateVersion: g.RateVersion, Billing: []billing.Segment{{Version: g.Billing.Version, Download: 100_000, AppliedAt: *now}}}
		ack, err := s.ReportOfflineGrant(g.NodeID, g.ID, u)
		if err != nil {
			t.Fatal("offline final lost", err)
		}
		if ack.NodeQuota.NodeUsedUnits != 15_000_000 || ack.NodeQuota.UsedUnits != int64(i+1)*15_000_000 || ack.Revoked != (i == 1) {
			t.Fatalf("wrong recovered snapshot: %+v", ack)
		}
		replay, err := s.ReportOfflineGrant(g.NodeID, g.ID, u)
		if err != nil || replay.NodeQuota.Revision != ack.NodeQuota.Revision {
			t.Fatal("replay changed watermark", err)
		}
		grants[i] = ack
	}
	firstGrant := grants[0]
	ack, err := s.ReportOfflineGrant(firstGrant.NodeID, firstGrant.ID, model.OfflineUsage{Version: 2, DownloadBytes: 100_000, Active: true, AppliedRateVersion: firstGrant.RateVersion})
	if err != nil || !ack.Revoked || ack.NodeQuota.UsedUnits != 30_000_000 {
		t.Fatal("other node did not learn aggregate exhaustion", err)
	}
	if _, err = s.AcquireNodeQuotaGrant(first.ID, strings.Repeat("c", 64), password, "vless"); !errors.Is(err, ErrEntitlement) {
		t.Fatal("exhausted subscription admitted", err)
	}
	for _, g := range grants {
		version := int64(2)
		if g.ID == firstGrant.ID {
			version = 3
		}
		if _, err = s.ReportOfflineGrant(g.NodeID, g.ID, model.OfflineUsage{Version: version, DownloadBytes: 100_000, Closed: true, AppliedRateVersion: g.RateVersion}); err != nil {
			t.Fatal("late final", err)
		}
	}
	e, err := s.UserSubscription(user, subscription)
	if err != nil || e.UsedBytes != 300_000 || e.ReservedBytes != 0 {
		t.Fatal("final aggregate", e, err)
	}
}

func TestNodeQuotaSnapshotRetainsLegacyReservations(t *testing.T) {
	quota := int64(9_000_000)
	s, _, node, password := offlineFixture(t, 3, &quota)
	old, err := s.AcquireOfflineGrant(node.ID, strings.Repeat("a", 64), password)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("b", 64), password, "hysteria2")
	if err != nil {
		t.Fatal(err)
	}
	if g.NodeQuota.LegacyReservedUnits != old.NormalBudgetBytes*100 {
		t.Fatal("legacy reservation was forgotten", g.NodeQuota)
	}
}

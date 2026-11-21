package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

// Real transactions and separate node credentials exercise subscription-wide
// deduplication; a node-local count could pass single-node tests incorrectly.
func TestOnlineIPSharedAcrossNodesProtocolsAndRelease(t *testing.T) {
	s, _, node, password := offlineFixture(t, 1, nil)
	first, err := s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "vless", "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}
	secondNode := newNode(t, s, "ip-second")
	if err = s.SetStatus(secondNode.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE subscription_allocations SET node_ids=? WHERE subscription_id=?", jsonString([]string{node.ID, secondNode.ID}), first.SubscriptionID); err != nil {
		t.Fatal(err)
	}
	secondPassword, err := s.PortalCredential(first.UserID, first.SubscriptionID, secondNode.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetOfflineAdmissionEnabled(secondNode.ID, true); err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquireNodeQuotaGrant(secondNode.ID, strings.Repeat("b", 64), secondPassword, "vless", "::ffff:198.51.100.1")
	if err != nil || second.ClientIP != first.ClientIP {
		t.Fatal("same IP across nodes/protocols rejected", second, err)
	}
	checkOccupied := func(want int) {
		t.Helper()
		e, err := s.UserSubscription(first.UserID, first.SubscriptionID)
		if err != nil || e.Occupied != want {
			t.Fatal("wrong occupied IPs", e, err)
		}
	}
	checkOccupied(1)
	if _, err = s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("h", 64), password, "hysteria2", first.ClientIP); !errors.Is(err, ErrSlots) {
		t.Fatal("HY2 instance inherited a VLESS IP slot", err)
	}
	if _, err = s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("c", 64), password, "vless", "198.51.100.2"); !errors.Is(err, ErrSlots) {
		t.Fatal("different IP bypassed cap", err)
	}
	if _, err = s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "vless", "198.51.100.2"); err == nil {
		t.Fatal("replay changed source IP")
	}
	for _, g := range []model.OfflineGrant{first, second} {
		ack, err := s.ReportOfflineGrant(g.NodeID, g.ID, model.OfflineUsage{Version: 1, SlotAction: "release"})
		if err != nil || !ack.SlotReleased {
			t.Fatal("release failed", err)
		}
		if g.ID == first.ID {
			checkOccupied(1)
		} else {
			checkOccupied(0)
		}
	}
	// A live same-IP grant permits cached reacquisition even at the IP cap.
	if _, err = s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("d", 64), password, "vless", first.ClientIP); err != nil {
		t.Fatal(err)
	}
	ack, err := s.ReportOfflineGrant(second.NodeID, second.ID, model.OfflineUsage{Version: 2, SlotAction: "acquire"})
	if err != nil || ack.SlotReleased {
		t.Fatal("same-IP reacquisition failed", err)
	}
	checkOccupied(1)
	if _, err = s.AcquireNodeQuotaGrant(secondNode.ID, strings.Repeat("e", 64), secondPassword, "vless", "2001:db8::1"); !errors.Is(err, ErrSlots) {
		t.Fatal("IPv6 bypassed cap", err)
	}
}

func TestOnlineIPConcurrentLastSlot(t *testing.T) {
	s, _, node, password := offlineFixture(t, 1, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := map[string]int{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ip := fmt.Sprintf("198.51.100.%d", i%3+1)
			_, err := s.AcquireNodeQuotaGrant(node.ID, fmt.Sprintf("%064d", i), password, "vless", ip)
			if err == nil {
				mu.Lock()
				accepted[ip]++
				mu.Unlock()
			} else if !errors.Is(err, ErrSlots) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(accepted) != 1 {
		t.Fatal("concurrent IPs bypassed cap", accepted)
	}
	for _, count := range accepted {
		if count != 4 {
			t.Fatal("same IP was limited as separate connections", accepted)
		}
	}
}

func TestOnlineIPLowerLimitKeepsWholeIPAndSharedBandwidth(t *testing.T) {
	s, _, user, template, node, password := portalFixture(t, 2, nil)
	var leases []model.ConnectionLease
	for i, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.1"} {
		l, err := s.AcquireProtocolLease(node.ID, fmt.Sprintf("%064d", i), password, "vless", ip)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	// Acknowledge actual rates before checking total allocation, including the
	// extra connection that shares an IP. IP dedup must never multiply bandwidth.
	for round := 0; round < 4; round++ {
		for i, l := range leases {
			var err error
			leases[i], err = s.RenewLease(node.ID, l.ID, model.LeaseUsage{AppliedRateVersion: l.RateVersion})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var ceiling, total int64
	if err := s.DB.QueryRow("SELECT SUM(ceiling) FROM portal_rates").Scan(&ceiling); err != nil {
		t.Fatal(err)
	}
	for _, l := range leases {
		if l.RatePending {
			t.Fatal("connection stayed paused", l)
		}
		total += l.BandwidthBytesPerSecond
	}
	if ceiling > 1_000_000 || total > 1_000_000 || total < 999_990 {
		t.Fatal("IP dedup changed shared bandwidth", total, ceiling)
	}
	template.MaxConnections = 1
	preview, err := s.PreviewSubscriptionTemplate(template.ID, template)
	if err != nil || len(preview.ConnectionsToClose) != 1 || preview.ConnectionsToClose[0].ID != leases[1].ID {
		t.Fatal("preview counted connections instead of IPs", preview, err)
	}
	if _, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	for i, l := range leases {
		_, err = s.RenewLease(node.ID, l.ID, model.LeaseUsage{AppliedRateVersion: l.RateVersion})
		if i == 1 && !errors.Is(err, ErrEntitlement) || i != 1 && err != nil {
			t.Fatal("wrong IP retained", i, err)
		}
	}
	// Revocation reserves the old IP until transport end is confirmed.
	e, err := s.UserSubscription(user.ID, leases[0].SubscriptionID)
	if err != nil || e.Occupied != 2 {
		t.Fatal("unconfirmed close released IP early", e, err)
	}
	if _, err = s.AcquireProtocolLease(node.ID, strings.Repeat("x", 64), password, "vless", "198.51.100.2"); !errors.Is(err, ErrSlots) {
		t.Fatal("revoked IP authorized a replacement while over the new cap", err)
	}
	if _, err = s.RenewLease(node.ID, leases[1].ID, model.LeaseUsage{Closed: true}); err != nil {
		t.Fatal(err)
	}
	e, _ = s.UserSubscription(user.ID, leases[0].SubscriptionID)
	if e.Occupied != 1 {
		t.Fatal("confirmed close retained IP", e)
	}
}

func TestOnlineIPNativeSessionsShareSubscriptionLimit(t *testing.T) {
	s, _, user, _, node, password := portalFixture(t, 1, nil)
	l, err := s.AcquireProtocolLease(node.ID, strings.Repeat("a", 64), password, "vless", "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		session := nativeSubscriptionSession(t, s, user.ID, l.SubscriptionID)
		credential, err := s.PortalCredential(user.ID, l.SubscriptionID, node.ID, session)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.AcquireProtocolLease(node.ID, fmt.Sprintf("%064d", i), credential, "vless", "198.51.100.1"); err != nil {
			t.Fatal("native session consumed a new IP slot", err)
		}
	}
	e, err := s.UserSubscription(user.ID, l.SubscriptionID)
	if err != nil || e.Occupied != 1 {
		t.Fatal("native and subscription scopes counted separately", e, err)
	}
	if _, err = s.AcquireProtocolLease(node.ID, strings.Repeat("b", 64), password, "hysteria2", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("store accepted missing ingress IP", err)
	}
}

func TestOnlineIPMigrationPreservesUnknownReservations(t *testing.T) {
	s, _, node, password := offlineFixture(t, 2, nil)
	for _, id := range []string{"a", "b"} {
		if _, err := s.AcquireOfflineGrant(node.ID, strings.Repeat(id, 64), password); err != nil {
			t.Fatal(err)
		}
	}
	// Recreate the pre-IP schema with real historical active leases.
	if _, err := s.DB.Exec("DROP INDEX portal_lease_ip; ALTER TABLE portal_leases DROP COLUMN client_ip; DELETE FROM metadata WHERE key IN ('online_ips_version','connection_slots_version')"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		tx, err := s.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = migrateOffline(tx, false); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("c", 64), password, "hysteria2", "198.51.100.1"); !errors.Is(err, ErrSlots) {
		t.Fatal("unknown historical IPs collapsed", err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE client_ip='' AND closed=0").Scan(&count); err != nil || count != 2 {
		t.Fatal("migration lost ledger", count, err)
	}
	if s.PortalSetting("connection_slots_version") != "3" {
		t.Fatal("rollback guard not advanced")
	}
}

package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

// Two HY2 instances and one VLESS IP share one subscription cap. A VLESS
// parallel connection shares its slot, while another HY2 instance cannot.
func TestOnlineInstancesShareLimitWithVLESS(t *testing.T) {
	s, _, node, password := offlineFixture(t, 3, nil)
	var grants []model.OfflineGrant
	for i, protocol := range []string{"hysteria2", "hysteria2", "vless", "vless"} {
		g, err := s.AcquireNodeQuotaGrant(node.ID, fmt.Sprintf("%064d", i), password, protocol, "198.51.100.1")
		if err != nil {
			t.Fatal(i, err)
		}
		grants = append(grants, g)
	}
	check := func(want int) {
		t.Helper()
		e, err := s.UserSubscription(grants[0].UserID, grants[0].SubscriptionID)
		if err != nil || e.Occupied != want || e.ReservedBytes != 0 {
			t.Fatal("incorrect shared occupancy or duplicated quota", e, err)
		}
	}
	check(3)
	for _, candidate := range []struct{ protocol, ip string }{{"hysteria2", "198.51.100.1"}, {"vless", "198.51.100.2"}} {
		if _, err := s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("x", 64), password, candidate.protocol, candidate.ip); !errors.Is(err, ErrSlots) {
			t.Fatal("full shared limit bypassed", candidate, err)
		}
	}
	// Replaying an issued connection at capacity must not consume another slot.
	replay, err := s.AcquireNodeQuotaGrant(node.ID, fmt.Sprintf("%064d", 0), password, "hysteria2", "198.51.100.1")
	if err != nil || replay.ID != grants[0].ID {
		t.Fatal("full-capacity replay failed", replay, err)
	}
	first, err := s.ReportOfflineGrant(node.ID, grants[0].ID, model.OfflineUsage{Version: 1, SlotAction: "release"})
	if err != nil || !first.SlotReleased {
		t.Fatal("HY2 slot did not release", first, err)
	}
	check(2)
	if _, err = s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("r", 64), password, "hysteria2", "198.51.100.1"); err != nil {
		t.Fatal("released slot unavailable", err)
	}
	check(3)
	// A released cached HY2 grant cannot share the replacement's same-IP slot.
	ack, err := s.ReportOfflineGrant(node.ID, first.ID, model.OfflineUsage{Version: 2, SlotAction: "acquire"})
	if err != nil || !ack.SlotReleased || ack.ReportVersion != 2 {
		t.Fatal("reacquisition bypassed limit or lost report acknowledgement", ack, err)
	}
	for i, g := range grants[2:] {
		if _, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 1, SlotAction: "release"}); err != nil {
			t.Fatal(err)
		}
		check(3 - i) // first VLESS disconnect retains the shared VLESS IP slot.
	}
	ack, err = s.ReportOfflineGrant(node.ID, first.ID, model.OfflineUsage{Version: 3, SlotAction: "acquire"})
	if err != nil || ack.SlotReleased {
		t.Fatal("HY2 could not reacquire the now-free slot", ack, err)
	}
	check(3)
}

func TestOnlineInstancesConcurrentSameIPLastSlot(t *testing.T) {
	s, _, node, password := offlineFixture(t, 1, nil)
	var accepted atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			_, err := s.AcquireNodeQuotaGrant(node.ID, fmt.Sprintf("%064d", i), password, "hysteria2", "198.51.100.1")
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrSlots) {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	if accepted.Load() != 1 {
		t.Fatal("same-IP HY2 instances raced past last slot", accepted.Load())
	}
}

func TestOnlineInstancesLowerLimitAndRenewal(t *testing.T) {
	s, _, user, template, node, password := portalFixture(t, 3, nil)
	var leases []model.ConnectionLease
	for i := 0; i < 3; i++ {
		l, err := s.AcquireProtocolLease(node.ID, fmt.Sprintf("%064d", i), password, "hysteria2", "198.51.100.1")
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	template.MaxConnections = 1
	preview, err := s.PreviewSubscriptionTemplate(template.ID, template)
	if err != nil || len(preview.ConnectionsToClose) != 2 {
		t.Fatal("plan preview merged HY2 instances", preview, err)
	}
	if _, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	for i, l := range leases {
		_, err = s.RenewLease(node.ID, l.ID, model.LeaseUsage{AppliedRateVersion: l.RateVersion})
		if i == 0 && err != nil || i > 0 && !errors.Is(err, ErrEntitlement) {
			t.Fatal("wrong HY2 instance retained", i, err)
		}
		if i > 0 {
			if _, err = s.RenewLease(node.ID, l.ID, model.LeaseUsage{Closed: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	e, err := s.UserSubscription(user.ID, leases[0].SubscriptionID)
	if err != nil || e.Occupied != 1 {
		t.Fatal("closed excess instances retained slots", e, err)
	}
}

func TestOnlineInstancesMigrationPreservesLedgerAndRejectsFutureVersion(t *testing.T) {
	s, _, node, password := offlineFixture(t, 3, nil)
	for i := 0; i < 2; i++ {
		if _, err := s.AcquireNodeQuotaGrant(node.ID, fmt.Sprintf("%064d", i), password, "hysteria2", "198.51.100.1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec("UPDATE metadata SET value='2' WHERE key='connection_slots_version'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := EnsureOfflineSchema(s.DB); err != nil {
			t.Fatal("existing IP schema did not upgrade idempotently", err)
		}
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE closed=0 AND client_ip='198.51.100.1'").Scan(&count); err != nil || count != 2 || s.PortalSetting("connection_slots_version") != "3" {
		t.Fatal("migration changed live accounting", count, err)
	}
	if _, err := s.DB.Exec("UPDATE metadata SET value='4' WHERE key='connection_slots_version'"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureOfflineSchema(s.DB); err == nil || s.PortalSetting("connection_slots_version") != "4" {
		t.Fatal("migration silently downgraded a future contract", err)
	}
}

// Simulate a v2 ledger that admitted three HY2 instances under one IP slot.
// v3 stops the newer instances on report, while still settling their real bytes.
func TestOnlineInstancesExistingOverLimitReportsAndLateFinal(t *testing.T) {
	s, now, node, password := offlineFixture(t, 3, nil)
	var grants []model.OfflineGrant
	for i := 0; i < 3; i++ {
		g, err := s.AcquireNodeQuotaGrant(node.ID, fmt.Sprintf("%064d", i), password, "hysteria2", "198.51.100.1")
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, g)
	}
	// Bypass normal template reconciliation only to model pre-upgrade state.
	if _, err := s.DB.Exec("UPDATE portal_templates SET config=json_set(config,'$.maxConnections',1)"); err != nil {
		t.Fatal(err)
	}
	for i := 2; i >= 0; i-- {
		g := grants[i]
		u := model.OfflineUsage{Version: 1, Active: true, DownloadBytes: 100, Billing: []billing.Segment{{Version: g.Billing.Version, Download: 100, AppliedAt: *now}}}
		ack, err := s.ReportOfflineGrant(node.ID, g.ID, u)
		if err != nil || ack.Revoked != (i > 0) || ack.DownloadBytes != 100 {
			t.Fatal("migration did not retain oldest instance and settle excess usage", i, ack, err)
		}
		if i > 0 {
			u.Version, u.Closed, u.Active, u.SlotAction = 2, true, false, "release"
			u.DownloadBytes, u.Billing[0].Download = 150, 150
			ack, err = s.ReportOfflineGrant(node.ID, g.ID, u)
			if err != nil || !ack.Closed || ack.DownloadBytes != 150 {
				t.Fatal("revoked instance lost late final", ack, err)
			}
		}
	}
	e, err := s.UserSubscription(grants[0].UserID, grants[0].SubscriptionID)
	if err != nil || e.Occupied != 1 || e.UsedBytes != 400 {
		t.Fatal("upgrade leaked slots or usage", e, err)
	}
}

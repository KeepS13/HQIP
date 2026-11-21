package store

import (
	"strings"
	"sync"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func TestOfflineSlotsReleaseReacquireAndSettleWhileFull(t *testing.T) {
	quota := int64(200_000_000)
	s, now, node, password := offlineFixture(t, 1, &quota)
	enableBilling(t, s, 150, 150)
	first, err := s.legacyNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "hysteria2")
	if err != nil {
		t.Fatal(err)
	}
	report := func(g model.OfflineGrant, version int64, action string, used int64) model.OfflineGrant {
		t.Helper()
		u := model.OfflineUsage{Version: version, SlotAction: action, DownloadBytes: used, Billing: []billing.Segment{{Version: 2, Download: used, AppliedAt: *now}}}
		ack, err := s.ReportOfflineGrant(node.ID, g.ID, u)
		if err != nil {
			t.Fatal(err)
		}
		return ack
	}
	first = report(first, 1, "release", 1000)
	var ceiling int64
	if err = s.DB.QueryRow("SELECT ceiling FROM portal_rates WHERE lease_id=?", first.ID).Scan(&ceiling); err != nil || ceiling != 0 || first.BandwidthBytesPerSecond != 0 || !first.RatePending {
		t.Fatal("released slot retained a usable rate", first, ceiling, err)
	}
	if !first.SlotReleased || first.Revoked || first.Closed || !first.OfflineUntil.After(*now) {
		t.Fatalf("identity was destroyed instead of releasing slot: %+v", first)
	}
	e, _ := s.UserSubscription(first.UserID, first.SubscriptionID)
	if e.Occupied != 0 || e.UsedBytes != 1500 || e.ReservedBytes != 0 {
		t.Fatalf("release: %+v", e)
	}
	second, err := s.legacyNodeQuotaGrant(node.ID, strings.Repeat("b", 64), password, "vless")
	if err != nil {
		t.Fatal("released slot still blocks other protocol", err)
	}
	first = report(first, 2, "acquire", 2000)
	if !first.SlotReleased {
		t.Fatal("full subscription acquired another slot")
	}
	e, _ = s.UserSubscription(first.UserID, first.SubscriptionID)
	if e.UsedBytes != 3000 || e.Occupied != 1 {
		t.Fatalf("full-slot response lost accounting: %+v", e)
	}
	first = report(first, 2, "acquire", 2000)
	second = report(second, 1, "release", 0)
	first = report(first, 3, "acquire", 2000)
	if first.SlotReleased {
		t.Fatal("freed slot cannot be reacquired")
	}
	if err = s.DB.QueryRow("SELECT ceiling FROM portal_rates WHERE lease_id=?", first.ID).Scan(&ceiling); err != nil || first.RatePending || ceiling == 0 || first.BandwidthBytesPerSecond > 0 && ceiling < first.BandwidthBytesPerSecond {
		t.Fatal("reacquisition failed to reserve its rate ceiling", first, ceiling, err)
	}
	e, _ = s.UserSubscription(first.UserID, first.SubscriptionID)
	if e.Occupied != 1 || e.UsedBytes != 3000 {
		t.Fatalf("reacquire repriced traffic: %+v", e)
	}
	// An idle heartbeat must not silently reserve a released slot again.
	second = report(second, 2, "", 0)
	if !second.SlotReleased {
		t.Fatal("heartbeat resurrected released slot")
	}
}

func TestOfflineSlotsConcurrentReacquisitionPreservesCap(t *testing.T) {
	quota := int64(200_000_000)
	s, _, node, password := offlineFixture(t, 1, &quota)
	var grants []model.OfflineGrant
	for _, ch := range []string{"a", "b"} {
		g, err := s.legacyNodeQuotaGrant(node.ID, strings.Repeat(ch, 64), password, "hysteria2")
		if err != nil {
			t.Fatal(err)
		}
		g, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 1, SlotAction: "release"})
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, g)
	}
	results := make(chan model.OfflineGrant, 2)
	var wg sync.WaitGroup
	for _, g := range grants {
		wg.Add(1)
		go func(g model.OfflineGrant) {
			defer wg.Done()
			ack, err := s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 2, SlotAction: "acquire"})
			if err != nil {
				t.Error(err)
				return
			}
			results <- ack
		}(g)
	}
	wg.Wait()
	close(results)
	held := 0
	for g := range results {
		if !g.SlotReleased {
			held++
		}
	}
	if held != 1 {
		t.Fatal("concurrent acquire bypassed cap", held)
	}
}

func TestOfflineSlotsReleaseRejectsLiveOrLegacyGrant(t *testing.T) {
	quota := int64(200_000_000)
	s, _, node, password := offlineFixture(t, 2, &quota)
	g, err := s.legacyNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "hysteria2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 1, SlotAction: "release", Active: true}); err == nil {
		t.Fatal("released active child")
	}
	old, err := s.AcquireOfflineGrant(node.ID, strings.Repeat("b", 64), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReportOfflineGrant(node.ID, old.ID, model.OfflineUsage{Version: 1, SlotAction: "release"}); err == nil {
		t.Fatal("legacy grant opted into unsupported slot lifecycle")
	}
}

func TestOfflineSlotsReacquireRejectsRevokedOrExhaustedSubscription(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		quota := int64(200_000_000)
		s, now, node, password := offlineFixture(t, 1, &quota)
		g, err := s.legacyNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "hysteria2")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 1, SlotAction: "release"}); err != nil {
			t.Fatal(err)
		}
		u := model.OfflineUsage{Version: 2, SlotAction: "acquire"}
		if revoke {
			e, _ := s.UserSubscription(g.UserID, g.SubscriptionID)
			if err = s.RevokeSubscription(g.UserID, g.SubscriptionID, e.Revision); err != nil {
				t.Fatal(err)
			}
		} else {
			// The node reports an offline excess in full before the denial.
			u.DownloadBytes = quota + 100
			u.Billing = []billing.Segment{{Version: g.Billing.Version, Download: u.DownloadBytes, AppliedAt: *now}}
		}
		g, err = s.ReportOfflineGrant(node.ID, g.ID, u)
		if err != nil || !g.Revoked || !g.SlotReleased {
			t.Fatal("cached parent bypassed subscription rejection", g, err)
		}
		e, _ := s.UserSubscription(g.UserID, g.SubscriptionID)
		if e.Occupied != 0 || !revoke && e.UsedBytes != quota+100 {
			t.Fatal("denial dropped offline usage or kept slot", e)
		}
		if _, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 3, SlotAction: "release", Closed: true, DownloadBytes: u.DownloadBytes}); err != nil {
			t.Fatal("terminal release could not close parent", err)
		}
	}
}

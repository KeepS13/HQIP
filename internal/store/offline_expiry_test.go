package store

import (
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Released parent ledgers are reusable identities, not live connections. Both
// release and reacquisition must return the latest entitlement deadline without
// resurrecting a slot or resetting accounting when the administrator edits it.
func TestOfflineSlotExpiryEditRefreshesIdleAuthorization(t *testing.T) {
	for _, change := range []time.Duration{-time.Hour, time.Hour} {
		t.Run(change.String(), func(t *testing.T) {
			quota := int64(200_000_000)
			s, now, node, password := offlineFixture(t, 1, &quota)
			g, err := s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "hysteria2", "192.0.2.1")
			if err != nil {
				t.Fatal(err)
			}
			g, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 1, SlotAction: "release"})
			if err != nil || !g.SlotReleased {
				t.Fatal(g, err)
			}
			e, _ := s.UserSubscription(g.UserID, g.SubscriptionID)
			deadline := e.ExpiresAt.Add(change)
			if _, err = s.UpdateSubscriptionExpiry(g.UserID, g.SubscriptionID, deadline, e.Revision); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Second)
			g, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 2})
			if err != nil || !g.OfflineUntil.Equal(deadline) || !g.SlotReleased {
				t.Fatal("idle receipt retained stale deadline or resurrected slot", g, err)
			}
			e, _ = s.UserSubscription(g.UserID, g.SubscriptionID)
			if e.Occupied != 0 || e.UsedBytes != 0 || e.ReservedBytes != 0 {
				t.Fatal("expiry edit changed accounting or occupied a device", e)
			}
			g, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 3, SlotAction: "acquire"})
			if err != nil || !g.OfflineUntil.Equal(deadline) || g.SlotReleased || g.Revoked {
				t.Fatal("reconnect did not use current authorization", g, err)
			}
			e, _ = s.UserSubscription(g.UserID, g.SubscriptionID)
			if e.Occupied != 1 {
				t.Fatal("reconnect failed to occupy exactly one slot", e)
			}
		})
	}
}

func TestOfflineSlotExpiryEditCannotReviveExpiredAuthorization(t *testing.T) {
	quota := int64(200_000_000)
	s, now, node, password := offlineFixture(t, 1, &quota)
	g, err := s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "hysteria2", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	g, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 1, SlotAction: "release"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.UserSubscription(g.UserID, g.SubscriptionID)
	if _, err = s.UpdateSubscriptionExpiry(g.UserID, g.SubscriptionID, now.Add(time.Minute), e.Revision); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Minute)
	g, err = s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 2, SlotAction: "acquire"})
	if err != nil || !g.Revoked || !g.SlotReleased {
		t.Fatal("stale cached identity bypassed the shortened expiry", g, err)
	}
	e, _ = s.UserSubscription(g.UserID, g.SubscriptionID)
	if e.Occupied != 0 {
		t.Fatal("expired authorization occupied a slot", e)
	}
}

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAdmissionIdentityChecksNeverReserveCapacity(t *testing.T) {
	s, now, node, password := offlineFixture(t, 1, nil)
	g, err := s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("a", 64), password, "hysteria2", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		account, err := s.CheckAdmissionIdentity(context.Background(), node.ID, password)
		if err != nil || account != "subscription_"+g.SubscriptionID {
			t.Fatal("full slots blocked identity-only check", account, err)
		}
	}
	var leases, grants int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=?", g.SubscriptionID).Scan(&leases)
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM portal_offline_grants").Scan(&grants)
	e, err := s.UserSubscription(g.UserID, g.SubscriptionID)
	if err != nil || leases != 1 || grants != 1 || e.Occupied != 1 || e.UsedBytes != 0 {
		t.Fatal("identity check changed accounting", leases, grants, e, err)
	}
	if _, err = s.AcquireNodeQuotaGrant(node.ID, strings.Repeat("b", 64), password, "hysteria2", "192.0.2.2"); !errors.Is(err, ErrSlots) {
		t.Fatal("identity receipt bypassed real admission", err)
	}
	for _, candidate := range []struct{ node, password string }{{"wrong-node", password}, {node.ID, strings.Repeat("x", 43)}} {
		if _, err = s.CheckAdmissionIdentity(context.Background(), candidate.node, candidate.password); err == nil {
			t.Fatal("wrong identity accepted")
		}
	}
	*now = now.Add(100 * 24 * time.Hour)
	if _, err = s.CheckAdmissionIdentity(context.Background(), node.ID, password); !errors.Is(err, ErrEntitlement) {
		t.Fatal("expired subscription accepted", err)
	}
}

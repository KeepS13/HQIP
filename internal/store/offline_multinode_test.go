package store

import (
	"context"
	"github.com/KeepS13/HQIP/internal/model"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOfflineMultiNodeSharesOneEmergencyPoolAcrossReconnects(t *testing.T) {
	quota := int64(64 << 20)
	s, _, n, password := offlineFixture(t, 2, &quota)
	var user, subscription string
	if err := s.DB.QueryRow("SELECT user_id,subscription_id FROM portal_credentials WHERE node_id=?", n.ID).Scan(&user, &subscription); err != nil {
		t.Fatal(err)
	}
	second := newNode(t, s, "offline-second")
	if err := s.SetStatus(second.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE subscription_allocations SET node_ids=? WHERE subscription_id=?", jsonString([]string{n.ID, second.ID}), subscription); err != nil {
		t.Fatal(err)
	}
	password2, err := s.PortalCredential(user, subscription, second.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetOfflineAdmissionEnabled(second.ID, true)
	var grants [2]model.OfflineGrant
	var wg sync.WaitGroup
	for i, v := range []struct{ node, password string }{{n.ID, password}, {second.ID, password2}} {
		wg.Add(1)
		go func(i int, node, password string) {
			defer wg.Done()
			g, e := s.AcquireOfflineGrant(node, strings.Repeat(string(rune('a'+i)), 64), password)
			if e != nil {
				t.Error(e)
			}
			grants[i] = g
		}(i, v.node, v.password)
	}
	wg.Wait()
	if grants[0].NormalBudgetBytes+grants[1].NormalBudgetBytes != quota || grants[0].EmergencyBudgetBytes+grants[1].EmergencyBudgetBytes != model.EmergencyAllowanceBytes {
		t.Fatal("cross-node double allocation", grants)
	}
	for _, g := range grants {
		u := model.OfflineUsage{Version: 1, DownloadBytes: g.NormalBudgetBytes + g.EmergencyBudgetBytes, EmergencyUsedBytes: g.EmergencyBudgetBytes, Closed: true}
		if _, err = s.ReportOfflineGrant(g.NodeID, g.ID, u); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ReportOfflineGrant(g.NodeID, g.ID, u); err != nil {
			t.Fatal(err)
		}
	}
	e, err := s.UserSubscription(user, subscription)
	if err != nil {
		t.Fatal(err)
	}
	if e.EmergencyUsedBytes != model.EmergencyAllowanceBytes || e.EmergencyReservedBytes != 0 || e.ReservedBytes != 0 || e.UsedBytes != quota+model.EmergencyAllowanceBytes {
		t.Fatal("incorrect aggregate settlement", e)
	}
	// Adding ordinary quota to this same subscription never resets its emergency pool.
	template, err := s.SubscriptionTemplate(e.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	newQuota := e.UsedBytes + quota
	template.QuotaBytes = &newQuota
	if _, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	g, err := s.AcquireOfflineGrant(n.ID, strings.Repeat("c", 64), password)
	if err != nil {
		t.Fatal(err)
	}
	if g.EmergencyBudgetBytes != 0 {
		t.Fatal("reconnect reset lifetime emergency allowance")
	}
}

func TestOfflineAdditiveMigrationIsIdempotentWithOpenReservations(t *testing.T) {
	q := int64(4 << 20)
	s, _, n, p := offlineFixture(t, 1, &q)
	g, err := s.AcquireOfflineGrant(n.ID, strings.Repeat("a", 64), p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if s.Postgres() {
			err = MigratePostgresOffline(context.Background(), s.postgresDSN)
		} else {
			err = EnsureOfflineSchema(s.DB)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	again, err := loadOfflineGrant(s.DB, n.ID, g.ID)
	if err != nil || again != g {
		t.Fatal("migration changed live permit", err)
	}
	e, err := s.UserSubscription(g.UserID, g.SubscriptionID)
	if err != nil || e.ReservedBytes != q || e.EmergencyReservedBytes != model.EmergencyAllowanceBytes {
		t.Fatal("migration reset accounting", err)
	}
}

func TestOfflineEarlySubscriptionExpiryAndClockRollback(t *testing.T) {
	s, now, n, p := offlineFixture(t, 1, nil)
	if _, err := s.DB.Exec("UPDATE portal_subscriptions SET expires=?", now.Add(10*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	g, err := s.AcquireOfflineGrant(n.ID, strings.Repeat("a", 64), p)
	if err != nil {
		t.Fatal(err)
	}
	if !g.OfflineUntil.Equal(now.Add(10 * time.Minute)) {
		t.Fatal("subscription expiry ignored")
	}
	*now = now.Add(-time.Second)
	g, err = s.ReportOfflineGrant(n.ID, g.ID, model.OfflineUsage{Version: 1, Active: true})
	if err != nil || !g.Revoked {
		t.Fatal("backwards control clock renewed permit", err)
	}
}

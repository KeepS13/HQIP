package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func nativeSubscriptionSession(t *testing.T, s *Store, user, subscription string) string {
	t.Helper()
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tokens, _, err := newPortalSession(tx, user, "native", "test", NewID(), s.Now())
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SelectSubscription(user, tokens.SessionID, subscription, 1); err != nil {
		t.Fatal(err)
	}
	return tokens.SessionID
}
func grantAnother(t *testing.T, s *Store, user string, p model.SubscriptionTemplate, key string) model.UserSubscription {
	t.Helper()
	// This helper represents grandfathered copies that could overlap before
	// per-purchase allocation. New allocation tests use AssignSubscription.
	id := NewID()
	audienceExec(t, s, "INSERT INTO portal_subscriptions(id,user_id,template_id,created_at,expires,grant_key) VALUES(?,?,?,?,?,?)", id, user, p.ID, s.Now().Unix(), s.Now().Add(time.Duration(p.ValidityDays)*24*time.Hour).Unix(), "legacy:"+key)
	audienceExec(t, s, "INSERT INTO portal_usage(subscription_id) VALUES(?)", id)
	audienceExec(t, s, "INSERT INTO subscription_periods(id,subscription_id,starts_at,ends_at) SELECT id,id,created_at,expires FROM portal_subscriptions WHERE id=?", id)
	audienceExec(t, s, "INSERT INTO subscription_allocations(subscription_id,target_count,node_ids) VALUES(?,?,?)", id, len(p.NodeIDs), jsonString(p.NodeIDs))
	audienceExec(t, s, "UPDATE portal_users SET revision=revision+1 WHERE id=?", user)
	e, err := s.UserSubscription(user, id)
	if err != nil {
		t.Fatal(err)
	}
	return *e
}
func subscriptionPassword(t *testing.T, s *Store, user, subscription, node string) string {
	t.Helper()
	if _, err := s.PortalSubscription(user, subscription, "create"); err != nil {
		t.Fatal(err)
	}
	p, err := s.PortalCredential(user, subscription, node, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSubscriptionsAreIndependentAcrossIdenticalTemplatePurchases(t *testing.T) {
	q := 2 * model.MinimumSubscriptionLeaseBytes
	s, now, u, p, n, passA := portalFixture(t, 1, &q)
	a := u.Subscriptions[0]
	*now = now.Add(time.Hour)
	b := grantAnother(t, s, u.ID, p, "second-copy")
	passB := subscriptionPassword(t, s, u.ID, b.ID, n.ID)
	if a.ID == b.ID || !b.ExpiresAt.Equal(a.ExpiresAt.Add(time.Hour)) || passA == passB {
		t.Fatal("copies do not have independent identity, expiry and credentials")
	}
	la := acquire(t, s, n, passA, "a")
	lb := acquire(t, s, n, passB, "b")
	if la.SubscriptionID != a.ID || lb.SubscriptionID != b.ID || la.AccountingID == lb.AccountingID || la.BandwidthBytesPerSecond != lb.BandwidthBytesPerSecond || la.RatePending || lb.RatePending {
		t.Fatal("independent copies shared a rate/identity pool")
	}
	for _, pass := range []string{passA, passB} {
		if _, err := s.AcquireLease(n.ID, strings.Repeat("c", 32), pass); !errors.Is(err, ErrSlots) {
			t.Fatal("copy concurrency not enforced", err)
		}
	}
	_ = renew(t, s, la, 300, 200, true)
	ea, _ := s.UserSubscription(u.ID, a.ID)
	eb, _ := s.UserSubscription(u.ID, b.ID)
	if ea.UsedBytes != 500 || ea.ReservedBytes != 0 || eb.UsedBytes != 0 || eb.ReservedBytes != q {
		t.Fatalf("cross-copy budget charge: %+v %+v", ea, eb)
	}
	if err := s.RevokeSubscription(u.ID, a.ID, ea.Revision); err != nil {
		t.Fatal(err)
	}
	lb = renew(t, s, lb, 10, 20, false)
	if lb.Closed {
		t.Fatal("removing A closed B")
	}
	if _, err := s.AcquireLease(n.ID, strings.Repeat("d", 32), passA); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("removed credential accepted", err)
	}
}

func TestSubscriptionRequestsApprovalCASIdempotenceAndHiddenAssignment(t *testing.T) {
	s, now, u, p, _, _ := portalFixture(t, 1, nil)
	p = allocationStockForTest(t, s, p, 4)
	r, err := s.CreateSubscriptionRequest(u.ID, p.ID, "purchase-key-1", p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.CreateSubscriptionRequest(u.ID, p.ID, "purchase-key-1", p.Revision)
	if err != nil || duplicate.ID != r.ID {
		t.Fatal("same purchase request was not idempotent")
	}
	independent, err := s.CreateSubscriptionRequest(u.ID, p.ID, "purchase-key-2", p.Revision)
	if err != nil || independent.ID == r.ID {
		t.Fatal("separate purchases of the same template were collapsed")
	}
	*now = now.Add(2 * time.Hour)
	refreshAllocationReports(t, s)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[string]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			approved, err := s.DecideSubscriptionRequest(r.ID, "approve", "confirmed", r.Revision, p.Revision)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[approved.SubscriptionID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != 1 {
		t.Fatal("approval created multiple instances")
	}
	subs, _ := s.UserSubscriptions(u.ID)
	if len(subs) != 2 || !subs[0].CreatedAt.Equal(*now) || !subs[0].ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatal("activation did not start at approval")
	}
	approved, err := s.DecideSubscriptionRequest(independent.ID, "approve", "second copy", independent.Revision, p.Revision)
	if err != nil || ids[approved.SubscriptionID] {
		t.Fatal("second pending purchase did not receive its own instance", err)
	}
	if subs, _ = s.UserSubscriptions(u.ID); len(subs) != 3 {
		t.Fatal("two approved requests did not activate two simultaneous copies")
	}
	r2, err := s.CreateSubscriptionRequest(u.ID, p.ID, "next-purchase", p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	p.Listed = false
	p.ValidityDays = 7
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideSubscriptionRequest(r2.ID, "approve", "", r2.Revision, p.Revision-1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale template approval accepted")
	}
	if _, err = s.CreateSubscriptionRequest(u.ID, p.ID, "hidden-buy-1", p.Revision); !errors.Is(err, ErrEntitlement) {
		t.Fatal("hidden template purchasable", err)
	}
	c := grantAnother(t, s, u.ID, p, "hidden-admin")
	if !c.ExpiresAt.Equal(now.Add(7 * 24 * time.Hour)) {
		t.Fatal("admin could not grant hidden template")
	}
	original, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if !original.ExpiresAt.Equal(u.Subscriptions[0].ExpiresAt) {
		t.Fatal("template validity changed existing expiry")
	}
}

func TestSubscriptionTemplateEditsApplyLimitsWithoutClearingUsage(t *testing.T) {
	s, _, u, p, n, password := portalFixture(t, 2, nil)
	a := acquire(t, s, n, password, "a")
	a = renew(t, s, a, 20, 30, false)
	b := acquire(t, s, n, password, "b")
	p.MaxConnections = 1
	impact, err := s.PreviewSubscriptionTemplate(p.ID, p)
	if err != nil || impact.AffectedSubscriptions != 1 || impact.AffectedUsers != 1 || len(impact.ConnectionsToClose) != 1 {
		t.Fatalf("bad preview %+v %v", impact, err)
	}
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewLease(n.ID, b.ID, model.LeaseUsage{}); !errors.Is(err, ErrEntitlement) {
		t.Fatal("newer connection survived reduced limit", err)
	}
	a = renew(t, s, a, 20, 30, false)
	limit := int64(49)
	p.QuotaBytes = &limit
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if e.UsedBytes != 50 || e.Status != "exhausted" {
		t.Fatalf("quota edit reset or hid usage %+v", e)
	}
	if _, err = s.RenewLease(n.ID, a.ID, model.LeaseUsage{UploadBytes: 20, DownloadBytes: 30}); !errors.Is(err, ErrEntitlement) {
		t.Fatal("exhausted copy continued")
	}
	_ = renew(t, s, a, 25, 35, true)
	e, _ = s.UserSubscription(u.ID, e.ID)
	if e.UsedBytes != 60 {
		t.Fatal("late final was not settled into revoked grant")
	}
}

func TestSubscriptionSessionSwitchDrainsAndRejectsStaleRequests(t *testing.T) {
	s, _, u, p, n, _ := portalFixture(t, 2, nil)
	a := u.Subscriptions[0]
	b := grantAnother(t, s, u.ID, p, "device-other-copy")
	first := nativeSubscriptionSession(t, s, u.ID, a.ID)
	other := nativeSubscriptionSession(t, s, u.ID, a.ID)
	pass, err := s.PortalCredential(u.ID, a.ID, n.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	otherPass, err := s.PortalCredential(u.ID, a.ID, n.ID, other)
	if err != nil {
		t.Fatal(err)
	}
	l := acquire(t, s, n, pass, "a")
	second := acquire(t, s, n, otherPass, "b")
	if _, err = s.SelectSubscription(u.ID, first, b.ID, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("switch ignored unclosed lease")
	}
	if err = s.DrainSubscriptionSession(u.ID, first, a.ID, 2); err != nil {
		t.Fatal(err)
	}
	_ = renew(t, s, l, 10, 20, true)
	selected, err := s.SelectSubscription(u.ID, first, b.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if selected.SelectionRevision != 3 {
		t.Fatal("selection version wrong")
	}
	same, err := s.SelectSubscription(u.ID, first, b.ID, 2)
	if err != nil || same.SelectionRevision != 3 {
		t.Fatal("idempotent selection changed revision")
	}
	if err = s.DrainSubscriptionSession(u.ID, first, a.ID, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("late drain accepted")
	}
	if _, _, err = s.PortalClientProfile(u.ID, first, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("stale subscription profile issued")
	}
	if _, err = s.AcquireLease(n.ID, strings.Repeat("c", 32), pass); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old session credential survived switch")
	}
	_ = renew(t, s, second, 5, 5, false)
	if _, err = s.PortalCredential(u.ID, a.ID, n.ID, other); err != nil {
		t.Fatal("another installation was disturbed", err)
	}
}

func TestSubscriptionNodeRemovalAndLowerLimitKeepOldestEligibleConnection(t *testing.T) {
	s, _, u, p, removed, oldPassword := portalFixture(t, 2, nil)
	kept := newNode(t, s, "retained-node")
	if _, err := s.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]' WHERE id=?", kept.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(kept.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	p.NodeIDs = append(p.NodeIDs, kept.ID)
	p, err := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	audienceExec(t, s, "UPDATE subscription_allocations SET node_ids=?,target_count=? WHERE subscription_id=?", jsonString(p.NodeIDs), len(p.NodeIDs), u.Subscriptions[0].ID)
	password := subscriptionPassword(t, s, u.ID, u.Subscriptions[0].ID, kept.ID)
	old := acquire(t, s, removed, oldPassword, "a")
	current := acquire(t, s, kept, password, "b")
	p.NodeIDs, p.DefaultNodeID, p.MaxConnections = []string{kept.ID}, kept.ID, 1
	impact, err := s.PreviewSubscriptionTemplate(p.ID, p)
	if err != nil || len(impact.ConnectionsToClose) != 1 || impact.ConnectionsToClose[0].ID != old.ID {
		t.Fatal("preview closed an eligible connection behind a removed node", impact, err)
	}
	if _, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewLease(removed.ID, old.ID, model.LeaseUsage{}); !errors.Is(err, ErrEntitlement) {
		t.Fatal("removed-node lease remained authorized", err)
	}
	current = renew(t, s, current, 0, 0, false)
	if current.Closed {
		t.Fatal("eligible remaining connection was closed")
	}
	if _, err = s.AcquireLease(kept.ID, strings.Repeat("c", 32), password); !errors.Is(err, ErrSlots) {
		t.Fatal("closing connection prematurely released admission slot", err)
	}
	_ = renew(t, s, old, 10, 20, true)
	_ = renew(t, s, current, 30, 40, false)
}

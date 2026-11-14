package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func refreshAllocationReports(t *testing.T, s *Store) {
	t.Helper()
	audienceExec(t, s, "UPDATE nodes SET last_seen=?,core_healthy=1", s.Now().Unix())
}

func allocationStockForTest(t *testing.T, s *Store, p model.SubscriptionTemplate, count int) model.SubscriptionTemplate {
	t.Helper()
	for i := 0; i < count; i++ {
		n := automaticTestNode(t, s, "stock-"+NewID())
		p.NodeIDs = append(p.NodeIDs, n.ID)
	}
	p.NodesPerSubscription = 1
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func allocationFixture(t *testing.T, count, per int) (*Store, *time.Time, model.PortalUser, model.SubscriptionTemplate) {
	t.Helper()
	s, now := fixture(t)
	ids := []string{}
	for i := 0; i < count; i++ {
		ids = append(ids, automaticTestNode(t, s, fmt.Sprintf("allocation-%d", i)).ID)
	}
	u, err := s.RegisterPortal("allocation@example.com", "test-allocation-password")
	if err != nil {
		t.Fatal(err)
	}
	price := int64(300)
	p, err := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "allocation", Description: "第一条\n第二条", NodesPerSubscription: per, NodeIDs: ids, Enabled: true, Listed: true, PriceCents: &price, ValidityDays: 7, MaxConnections: 2}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s, now, u, p
}

func assignAllocation(t *testing.T, s *Store, user string, p model.SubscriptionTemplate) model.UserSubscription {
	t.Helper()
	e, err := s.AssignSubscription(user, p.ID, NewID(), -1, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSubscriptionAllocationInventoryAndRelease(t *testing.T) {
	s, now, u, p := allocationFixture(t, 6, 2)
	seen := map[string]bool{}
	subs := []model.UserSubscription{}
	for i := 0; i < 3; i++ {
		catalog, err := s.SubscriptionCatalog(u.ID)
		if err != nil || catalog[0].AvailableCopies != 3-i {
			t.Fatal(catalog, err)
		}
		e := assignAllocation(t, s, u.ID, p)
		subs = append(subs, e)
		if len(e.NodeIDs) != 2 {
			t.Fatal(e)
		}
		for _, id := range e.NodeIDs {
			if seen[id] {
				t.Fatal("overlap", id)
			}
			seen[id] = true
		}
	}
	if _, err := s.AssignSubscription(u.ID, p.ID, NewID(), -1, p.Revision); !errors.Is(err, ErrSubscriptionStock) {
		t.Fatal(err)
	}
	quota := int64(1)
	p.QuotaBytes = &quota
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignSubscription(u.ID, p.ID, NewID(), -1, p.Revision); !errors.Is(err, ErrSubscriptionStock) {
		t.Fatal("exhausted subscription released stock", err)
	}
	other, err := s.RegisterPortal("other-allocation@example.com", "test-allocation-password")
	if err != nil {
		t.Fatal(err)
	}
	assignAllocation(t, s, other.ID, p)
	second := p
	second.ID = ""
	second.Name = "another template"
	second, err = s.SaveSubscriptionTemplate("", second, 0)
	if err != nil {
		t.Fatal(err)
	}
	assignAllocation(t, s, u.ID, second)
	audienceExec(t, s, "UPDATE portal_subscriptions SET expires=? WHERE id=?", now.Unix(), subs[0].ID)
	refreshAllocationReports(t, s)
	replacement := assignAllocation(t, s, u.ID, p)
	for _, id := range replacement.NodeIDs {
		if id != subs[0].NodeIDs[0] && id != subs[0].NodeIDs[1] {
			t.Fatal("reused occupied node", replacement)
		}
	}
	current, _ := s.UserSubscription(u.ID, subs[1].ID)
	if err = s.RevokeSubscription(u.ID, current.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	assignAllocation(t, s, u.ID, p)
}

func TestSubscriptionAllocationMetadataAndPoolChanges(t *testing.T) {
	s, _, u, p := allocationFixture(t, 6, 2)
	a := assignAllocation(t, s, u.ID, p)
	b := assignAllocation(t, s, u.ID, p)
	p.Description = "更新介绍\n<script>plain text</script>"
	p.NodesPerSubscription = 1
	before, _ := s.PortalUser(u.ID)
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.PortalUser(u.ID)
	kept, _ := s.UserSubscription(u.ID, a.ID)
	if after.Revision != before.Revision || kept.Revision != a.Revision || !sameNodeSet(kept.NodeIDs, a.NodeIDs) || kept.NodesPerSubscription != 2 {
		t.Fatal("metadata changed existing rights", after, kept)
	}
	c := assignAllocation(t, s, u.ID, p)
	if len(c.NodeIDs) != 1 {
		t.Fatal(c)
	}
	removed := a.NodeIDs[0]
	pool := []string{}
	for _, id := range p.NodeIDs {
		if id != removed {
			pool = append(pool, id)
		}
	}
	p.NodeIDs = pool
	preview, err := s.PreviewSubscriptionTemplate(p.ID, p)
	if err != nil || len(preview.AllocationShortfalls) != 0 {
		t.Fatal(preview, err)
	}
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	kept, _ = s.UserSubscription(u.ID, a.ID)
	other, _ := s.UserSubscription(u.ID, b.ID)
	if len(kept.NodeIDs) != 2 || !sameNodeSet(other.NodeIDs, b.NodeIDs) {
		t.Fatal("failed to preserve and replenish", kept, other)
	}
	if _, err = s.PortalCredential(u.ID, a.ID, removed, "subscription"); !errors.Is(err, ErrEntitlement) {
		t.Fatal("removed node authorized", err)
	}
	p.NodeIDs = []string{b.NodeIDs[0]}
	preview, err = s.PreviewSubscriptionTemplate(p.ID, p)
	if err != nil || len(preview.AllocationShortfalls) == 0 {
		t.Fatal("missing shortage", preview, err)
	}
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil || len(p.AllocationShortfalls) == 0 {
		t.Fatal("save shortage missing", p, err)
	}
	for _, description := range []string{"", strings.Repeat("字", 2000)} {
		p.Description = description
		p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	p.Description = strings.Repeat("字", 2001)
	if _, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision); err == nil {
		t.Fatal("oversize description accepted")
	}
}

func TestSubscriptionAllocationRankingAndRandomTies(t *testing.T) {
	candidates := []subscriptionNodeCandidate{{id: "busy", remaining: 999, expires: 1}, {id: "less", remaining: 4, expires: 1}, {id: "later", remaining: 9, expires: 20}, {id: "best", remaining: 9, expires: 10}}
	a := &allocationSnapshot{candidates: candidates, holders: []*nodeAllocation{{user: "existing", template: "x", nodes: []string{"busy"}}, {user: "existing", template: "y", nodes: []string{"busy"}}}}
	h := &nodeAllocation{user: "new", template: "x", target: 4}
	a.fill(h, []string{"busy", "less", "later", "best"})
	if strings.Join(h.nodes, ",") != "best,later,less,busy" {
		t.Fatal(h.nodes)
	}
	// Deduplicate one user who holds several copies, including reservations.
	a.holders = append(a.holders, &nodeAllocation{user: "second", template: "z", nodes: []string{"less"}, order: true})
	if got := a.choices("new", "x", []string{"busy"}, nil); len(got) != 1 || got[0].users != 1 {
		t.Fatal(got)
	}
	a = &allocationSnapshot{candidates: []subscriptionNodeCandidate{{id: "a"}, {id: "b"}, {id: "c"}}}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		h = &nodeAllocation{user: "new", target: 1}
		a.fill(h, []string{"a", "b", "c"})
		seen[h.nodes[0]] = true
	}
	if len(seen) < 2 {
		t.Fatal("ties were never randomized")
	}
}

func TestSubscriptionAllocationConcurrentOrdersAndReservationLifecycle(t *testing.T) {
	s, now, u, p := allocationFixture(t, 6, 2)
	secret := "test-payment-secret-v1"
	if _, err := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", Secret: &secret, PayType: "aloop", PayDurationMinutes: 10}, "test"); err != nil {
		t.Fatal(err)
	}
	other, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.Now = s.Now
	var wg sync.WaitGroup
	var mu sync.Mutex
	orders := []model.PaymentOrder{}
	outOfStock := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 1 {
				db = other
			}
			o, e := db.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: fmt.Sprintf("parallel-%d", i), TemplateID: p.ID, TemplateRevision: p.Revision}, paymentTestClient())
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(e, ErrSubscriptionStock) {
				outOfStock++
			} else if e != nil {
				t.Error(e)
			} else {
				orders = append(orders, o)
			}
		}(i)
	}
	wg.Wait()
	if len(orders) != 3 || outOfStock != 1 {
		t.Fatal(len(orders), outOfStock)
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].ID < orders[j].ID })
	if _, err = s.ClosePaymentOrder(u.ID, orders[0].ID, orders[0].Revision); err != nil {
		t.Fatal(err)
	}
	replacement := makePaymentOrder(t, s, u, p, "replacement-order")
	if err = s.ProcessPaymentNotify(paymentNotice(orders[0], secret)); err != nil {
		t.Fatal(err)
	}
	if n, e := s.FulfillPaymentOrders(20); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	blocked, _ := s.PaymentOrder(u.ID, orders[0].ID)
	if blocked.FulfillmentState != "blocked" {
		t.Fatal(blocked)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(replacement, secret)); err != nil {
		t.Fatal(err)
	}
	// A reservation is honored after temporary telemetry loss.
	*now = now.Add(time.Minute)
	if n, e := s.FulfillPaymentOrders(20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	*now = now.Add(10 * time.Minute)
	refreshAllocationReports(t, s)
	catalog, err := s.SubscriptionCatalog(u.ID)
	if err != nil || catalog[0].AvailableCopies != 2 {
		t.Fatal(catalog, err)
	}
	if _, err = s.RetryPaymentFulfillment(blocked.ID, blocked.Revision, "test"); err != nil {
		t.Fatal(err)
	}
	fulfilled, _ := s.PaymentOrder(u.ID, blocked.ID)
	if fulfilled.Status != "fulfilled" {
		t.Fatal(fulfilled)
	}
}

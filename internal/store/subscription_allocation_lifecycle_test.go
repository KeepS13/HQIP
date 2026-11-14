package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
)

func enableAllocationPayments(t *testing.T, s *Store) string {
	t.Helper()
	secret := "test-payment-secret-v1"
	if _, err := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", Secret: &secret, PayType: "aloop", PayDurationMinutes: 10}, "test"); err != nil {
		t.Fatal(err)
	}
	return secret
}

func allocationReservation(t *testing.T, s *Store, order string) (int, []string) {
	t.Helper()
	var target int
	var raw string
	if err := s.DB.QueryRow("SELECT target_count,node_ids FROM payment_node_reservations WHERE order_id=?", order).Scan(&target, &raw); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		t.Fatal(err)
	}
	return target, ids
}

func TestSubscriptionAllocationMixedConcurrentGrantsAndOrders(t *testing.T) {
	s, _, u, p := allocationFixture(t, 6, 2)
	enableAllocationPayments(t, s)
	other, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.Now = s.Now
	results := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, e := s.AssignSubscription(u.ID, p.ID, fmt.Sprintf("mixed-admin-%d", i), -1, p.Revision)
				results <- e
			} else {
				_, e := other.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: fmt.Sprintf("mixed-order-%d", i), TemplateID: p.ID, TemplateRevision: p.Revision}, paymentTestClient())
				results <- e
			}
		}(i)
	}
	wg.Wait()
	close(results)
	succeeded, unavailable := 0, 0
	for e := range results {
		if e == nil {
			succeeded++
		} else if errors.Is(e, ErrSubscriptionStock) {
			unavailable++
		} else {
			t.Fatal(e)
		}
	}
	if succeeded != 3 || unavailable != 1 {
		t.Fatal(succeeded, unavailable)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	holders, err := allocationHolders(tx, s.Now())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, h := range holders {
		for _, id := range h.nodes {
			if seen[id] {
				t.Fatal("concurrent order and grant overlap", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 6 {
		t.Fatal("incomplete allocations", seen)
	}
}

func TestSubscriptionAllocationPoolReconciliationPreservesOrderTarget(t *testing.T) {
	s, now, u, p := allocationFixture(t, 6, 2)
	secret := enableAllocationPayments(t, s)
	a := assignAllocation(t, s, u.ID, p)
	*now = now.Add(time.Second)
	b := assignAllocation(t, s, u.ID, p)
	*now = now.Add(time.Second)
	o := makePaymentOrder(t, s, u, p, "pool-order-original")
	p.Description = "later description"
	p.NodesPerSubscription = 1
	p.NodeIDs = nil
	for i := 0; i < 3; i++ {
		p.NodeIDs = append(p.NodeIDs, automaticTestNode(t, s, fmt.Sprintf("replacement-%d", i)).ID)
	}
	preview, err := s.PreviewSubscriptionTemplate(p.ID, p)
	if err != nil || len(preview.AllocationShortfalls) != 2 {
		t.Fatal(preview, err)
	}
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil || len(p.AllocationShortfalls) != 2 {
		t.Fatal(p, err)
	}
	first, _ := s.UserSubscription(u.ID, a.ID)
	second, _ := s.UserSubscription(u.ID, b.ID)
	target, reserved := allocationReservation(t, s, o.ID)
	if len(first.NodeIDs) != 2 || len(second.NodeIDs) != 1 || target != 2 || len(reserved) != 0 {
		t.Fatal("oldest subscriptions must fill before orders", first, second, target, reserved)
	}
	for i := 0; i < 3; i++ {
		p.NodeIDs = append(p.NodeIDs, automaticTestNode(t, s, fmt.Sprintf("restored-%d", i)).ID)
	}
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil || len(p.AllocationShortfalls) != 0 {
		t.Fatal(p, err)
	}
	retained, _ := s.UserSubscription(u.ID, a.ID)
	if !sameNodeSet(retained.NodeIDs, first.NodeIDs) {
		t.Fatal("surviving nodes changed", first, retained)
	}
	target, reserved = allocationReservation(t, s, o.ID)
	if target != 2 || len(reserved) != 2 {
		t.Fatal("order target followed new template count", target, reserved)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(o, secret)); err != nil {
		t.Fatal(err)
	}
	if n, e := s.FulfillPaymentOrders(10); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	paid, _ := s.PaymentOrder(u.ID, o.ID)
	granted, _ := s.UserSubscription(u.ID, paid.SubscriptionID)
	if !sameNodeSet(granted.NodeIDs, reserved) || granted.NodesPerSubscription != 2 || paid.TemplateSnapshot.Description != o.TemplateSnapshot.Description {
		t.Fatal("order snapshot or reserved nodes changed", paid, granted)
	}
}

func TestSubscriptionAllocationUnknownCreationAndPaidHoldSurviveDisable(t *testing.T) {
	s, now, u, p := allocationFixture(t, 2, 2)
	secret := enableAllocationPayments(t, s)
	client := &payment.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated timeout")
	})}
	o, err := s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: "unknown-allocation-order", TemplateID: p.ID, TemplateRevision: p.Revision}, client)
	if err != nil || o.Status != "unknown" {
		t.Fatal(o, err)
	}
	catalog, err := s.SubscriptionCatalog(u.ID)
	if err != nil || catalog[0].AvailableCopies != 0 {
		t.Fatal("unknown creation released reservation", catalog, err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(o, secret)); err != nil {
		t.Fatal(err)
	}
	audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", u.ID)
	if n, e := s.FulfillPaymentOrders(10); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	*now = now.Add(11 * time.Minute)
	refreshAllocationReports(t, s)
	catalog, err = s.SubscriptionCatalog(u.ID)
	if err != nil || catalog[0].AvailableCopies != 0 {
		t.Fatal("disabled user's paid hold expired", catalog, err)
	}
	audienceExec(t, s, "UPDATE portal_users SET enabled=1 WHERE id=?", u.ID)
	blocked, _ := s.PaymentOrder(u.ID, o.ID)
	if _, err = s.RetryPaymentFulfillment(o.ID, blocked.Revision, "test"); err != nil {
		t.Fatal(err)
	}
	paid, _ := s.PaymentOrder(u.ID, o.ID)
	p.Enabled = false
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	p.Enabled = true
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignSubscription(u.ID, p.ID, "after-temporary-disable", -1, p.Revision); !errors.Is(err, ErrSubscriptionStock) {
		t.Fatal("temporary disable released subscription", paid, err)
	}
}

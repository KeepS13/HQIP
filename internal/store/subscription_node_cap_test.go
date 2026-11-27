package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

// The configured ceiling is preserved; inventory and saved allocations use the
// selected pool size, never the smaller number of currently healthy/free nodes.
func TestSubscriptionNodeCapCatalogAndGrant(t *testing.T) {
	for _, configured := range []int{2, 3, 10} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			s, _, user, p := allocationFixture(t, 3, configured)
			want := min(configured, 3)
			catalog, err := s.SubscriptionCatalog(user.ID)
			if err != nil || len(catalog) != 1 || catalog[0].AvailableCopies != 3/want || catalog[0].NodesPerSubscription != configured {
				t.Fatal(catalog, err)
			}
			sub := assignAllocation(t, s, user.ID, p)
			if sub.NodesPerSubscription != want || len(sub.NodeIDs) != want {
				t.Fatal(sub)
			}
			if configured >= 3 && !sameNodeSet(sub.NodeIDs, p.NodeIDs) {
				t.Fatal("all selected nodes must be assigned", sub.NodeIDs)
			}
			if _, err = s.AssignSubscription(user.ID, p.ID, NewID(), -1, p.Revision); !errors.Is(err, ErrSubscriptionStock) {
				t.Fatal("must not lower the target to remaining unoccupied inventory", err)
			}
		})
	}
}

func TestSubscriptionNodeCapPaymentKeepsPurchasedEffectiveCount(t *testing.T) {
	s, _, user, p := allocationFixture(t, 3, 10)
	secret := enableAllocationPayments(t, s)
	order := makePaymentOrder(t, s, user, p, "capped-order")
	target, ids := allocationReservation(t, s, order.ID)
	if target != 3 || !sameNodeSet(ids, p.NodeIDs) || order.TemplateSnapshot.NodesPerSubscription != 10 {
		t.Fatal(target, ids, order.TemplateSnapshot)
	}
	p.NodeIDs = append(p.NodeIDs, automaticTestNode(t, s, NewID()).ID)
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(order, secret)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FulfillPaymentOrders(10); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	paid, err := s.PaymentOrder(user.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.UserSubscription(user.ID, paid.SubscriptionID)
	if err != nil || sub.NodesPerSubscription != 3 || !sameNodeSet(sub.NodeIDs, ids) {
		t.Fatal(sub, err)
	}
}

func TestSubscriptionNodeCapApplyAndLaterExpansion(t *testing.T) {
	s, _, user, p := allocationFixture(t, 3, 1)
	sub := assignAllocation(t, s, user.ID, p)
	p.NodesPerSubscription = 10
	for _, want := range []int{3, 4} {
		if want == 4 {
			p.NodeIDs = append(p.NodeIDs, automaticTestNode(t, s, NewID()).ID)
		}
		var err error
		p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.ApplySubscriptionTemplate(p.ID, p.Revision)
		if err != nil || result.UpdatedSubscriptions != 1 || len(result.AllocationShortfalls) != 0 {
			t.Fatal(result, err)
		}
		current, err := s.UserSubscription(user.ID, sub.ID)
		if err != nil || current.NodesPerSubscription != want || !sameNodeSet(current.NodeIDs, p.NodeIDs) || !current.ExpiresAt.Equal(sub.ExpiresAt) {
			t.Fatal(current, err)
		}
		if result, err = s.ApplySubscriptionTemplate(p.ID, p.Revision); err != nil || result.UpdatedSubscriptions != 0 || len(result.AllocationShortfalls) != 0 {
			t.Fatal("replay changed the capped allocation", result, err)
		}
	}
}

func TestSubscriptionNodeCapDoesNotHideOccupiedNodes(t *testing.T) {
	s, _, user, p := allocationFixture(t, 3, 1)
	a := assignAllocation(t, s, user.ID, p)
	b := assignAllocation(t, s, user.ID, p)
	p.NodesPerSubscription = 10
	p, err := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplySubscriptionTemplate(p.ID, p.Revision)
	if err != nil || len(result.AllocationShortfalls) != 2 {
		t.Fatal(result, err)
	}
	seen := map[string]bool{}
	for _, old := range []model.UserSubscription{a, b} {
		current, err := s.UserSubscription(user.ID, old.ID)
		if err != nil || current.NodesPerSubscription != 3 {
			t.Fatal(current, err)
		}
		for _, id := range current.NodeIDs {
			if seen[id] {
				t.Fatal("same user's allocations overlap", id)
			}
			seen[id] = true
		}
	}
}

package store

import (
	"errors"
	"testing"
	"time"
)

func TestSubscriptionNodeCapDoesNotConvertOfflineNodesToSmallerPurchase(t *testing.T) {
	s, now, user, p := allocationFixture(t, 3, 10)
	*now = now.Add(time.Minute) // Reports are stale; the selected pool still has three nodes.
	catalog, err := s.SubscriptionCatalog(user.ID)
	if err != nil || len(catalog) != 1 || catalog[0].AvailableCopies != 0 {
		t.Fatal(catalog, err)
	}
	if _, err = s.AssignSubscription(user.ID, p.ID, NewID(), -1, p.Revision); !errors.Is(err, ErrSubscriptionStock) {
		t.Fatal("offline pool granted a smaller subscription", err)
	}
}

func TestSubscriptionNodeCapLatePaymentPreservesReservedTarget(t *testing.T) {
	s, _, user, p := allocationFixture(t, 3, 10)
	secret := enableAllocationPayments(t, s)
	order := makePaymentOrder(t, s, user, p, "capped-late-payment")
	if _, err := s.ClosePaymentOrder(user.ID, order.ID, order.Revision); err != nil {
		t.Fatal(err)
	}
	p.NodeIDs = p.NodeIDs[:2]
	p, err := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(order, secret)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FulfillPaymentOrders(10); err != nil || n != 0 {
		t.Fatal("late payment lowered the purchased count", n, err)
	}
	current, err := s.PaymentOrder(user.ID, order.ID)
	if err != nil || current.FulfillmentState != "blocked" {
		t.Fatal(current, err)
	}
	target, _ := allocationReservation(t, s, order.ID)
	if target != 3 {
		t.Fatal("released reservation lost its snapshot", target)
	}
}

func TestSubscriptionNodeCapManualEmptyPoolRetainsConfiguredCount(t *testing.T) {
	s, _, user, p := allocationFixture(t, 0, 10)
	sub := assignAllocation(t, s, user.ID, p)
	if sub.NodesPerSubscription != 10 || !sub.ActivationPending || len(sub.NodeIDs) != 0 {
		t.Fatal("manual provisioning count was capped to zero", sub)
	}
}

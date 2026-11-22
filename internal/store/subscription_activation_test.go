package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCustomPurchaseStartsOnlyOnManualActivation(t *testing.T) {
	s, now, user, template, _ := paymentFixture(t)
	initial, err := s.UserSubscriptions(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	node := template.NodeIDs[0]
	template.NodeIDs = []string{}
	template.ValidityDays = 7
	template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.SubscriptionCatalog(user.ID)
	if err != nil || len(catalog) != 1 || !catalog[0].ManualProvisioning {
		t.Fatal(catalog, err)
	}
	order := makePaymentOrder(t, s, user, template, "custom-purchase-key")
	// Mode, duration and count are immutable even across edits before receipt.
	template.NodeIDs, template.ValidityDays, template.NodesPerSubscription = []string{node}, 30, 2
	template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FulfillPaymentOrders(25); err != nil {
		t.Fatal(err)
	}
	order, err = s.PaymentOrder(user.ID, order.ID)
	if err != nil || order.FulfillmentState != "fulfilled" {
		t.Fatal(order, err)
	}
	sub, err := s.UserSubscription(user.ID, order.SubscriptionID)
	if err != nil || sub.Status != "pending" || !sub.ActivationPending || sub.Renewable || sub.ValidityDays != 7 || sub.NodesPerSubscription != 1 || !sub.ExpiresAt.IsZero() || len(sub.Periods) != 0 || len(sub.NodeIDs) != 0 || sub.Occupied != 0 {
		t.Fatal(sub, err)
	}
	if _, err = s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(template, *sub, "pending-renewal"), paymentTestClient()); !errors.Is(err, ErrSubscriptionRenewal) {
		t.Fatal("pending renewal allowed", err)
	}
	link, err := s.PortalSubscription(user.ID, sub.ID, "create")
	if err != nil || link == "" {
		t.Fatal(link, err)
	}
	owner, ids, err := s.ResolvePortalImport(strings.TrimPrefix(link, s.BaseURL+"/usub/"))
	if err != nil || owner != user.ID || len(ids) != 1 || ids[0] != sub.ID {
		t.Fatal(owner, ids, err)
	}
	combined, err := s.PortalAllSubscriptions(user.ID, "create")
	if err != nil || combined == "" {
		t.Fatal(combined, err)
	}
	profile, _, err := s.PortalClientProfile(user.ID, "subscription", sub.ID)
	if err != nil || len(profile.Nodes) != 0 {
		t.Fatal("pending export exposed nodes", profile, err)
	}
	if _, err = s.PortalCredential(user.ID, sub.ID, node, "subscription"); !errors.Is(err, ErrEntitlement) {
		t.Fatal("pending credentials allowed", err)
	}
	*now = now.Add(60 * 24 * time.Hour)
	refreshAllocationReports(t, s)
	sub, err = s.UserSubscription(user.ID, sub.ID)
	if err != nil || sub.Status != "pending" || sub.Occupied != 0 {
		t.Fatal("wait consumed term", sub, err)
	}
	if _, err = s.UpdateSubscriptionNodes(user.ID, sub.ID, []string{}, sub.Revision); !errors.Is(err, ErrSubscriptionNodes) {
		t.Fatal(err)
	}
	activated, err := s.UpdateSubscriptionNodes(user.ID, sub.ID, []string{node}, sub.Revision)
	if err != nil || activated.ActivationPending || activated.Status != "active" || activated.ActivatedAt == nil || !activated.ActivatedAt.Equal(*now) || !activated.ExpiresAt.Equal(now.Add(7*24*time.Hour)) || len(activated.Periods) != 1 || !activated.CurrentPeriodStartsAt.Equal(*now) {
		t.Fatal(activated, err)
	}
	if got, err := s.PortalSubscription(user.ID, sub.ID, "get"); err != nil || got != link {
		t.Fatal("activation rotated link", got, err)
	}
	profile, _, err = s.PortalClientProfile(user.ID, "subscription", sub.ID)
	if err != nil || len(profile.Nodes) == 0 {
		t.Fatal("same link did not acquire nodes", profile, err)
	}
	// Template pool edits must never remove custom nodes or reset the term.
	template.NodeIDs = []string{}
	template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || !sameNodeSet(before.NodeIDs, []string{node}) || !before.ExpiresAt.Equal(activated.ExpiresAt) {
		t.Fatal(before, err)
	}
	password := subscriptionPassword(t, s, user.ID, sub.ID, node)
	for i := 0; i < before.MaxConnections; i++ {
		if _, err = s.AcquireLease(node, NewID(), password); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.AcquireLease(node, NewID(), password); !errors.Is(err, ErrSlots) {
		t.Fatal("device limit bypassed", err)
	}
	before, _ = s.UserSubscription(user.ID, sub.ID)
	paidRenewal(t, s, template, *before, "custom-active-renewal")
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || after.ID != sub.ID || !sameNodeSet(after.NodeIDs, []string{node}) || after.Occupied != before.MaxConnections || !after.ExpiresAt.Equal(before.ExpiresAt.Add(30*24*time.Hour)) {
		t.Fatal("custom renewal changed rights", after, err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FulfillPaymentOrders(25); err != nil {
		t.Fatal(err)
	}
	all, err := s.UserSubscriptions(user.ID)
	if err != nil || len(all) != len(initial)+1 {
		t.Fatal("receipt replay created another grant", all, err)
	}
}

func TestCustomActivationConcurrentAndIdempotent(t *testing.T) {
	s, now, user, template := allocationFixture(t, 0, 1)
	sub := assignAllocation(t, s, user.ID, template)
	node := automaticTestNode(t, s, "custom-activation-node")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateSubscriptionNodes(user.ID, sub.ID, []string{node.ID}, sub.Revision)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for err := range errs {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	current, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || len(current.Periods) != 1 {
		t.Fatal(current, err)
	}
	*now = now.Add(time.Hour)
	unchanged, err := s.UpdateSubscriptionNodes(user.ID, sub.ID, current.NodeIDs, current.Revision)
	if err != nil || unchanged.Revision != current.Revision || !unchanged.ExpiresAt.Equal(current.ExpiresAt) || !unchanged.ActivatedAt.Equal(*current.ActivatedAt) {
		t.Fatal(unchanged, err)
	}
}

func TestCustomActivationDisabledAndRevokedCannotStart(t *testing.T) {
	for _, state := range []string{"user", "template", "revoked"} {
		t.Run(state, func(t *testing.T) {
			s, _, user, template := allocationFixture(t, 0, 1)
			sub := assignAllocation(t, s, user.ID, template)
			node := automaticTestNode(t, s, "custom-blocked-node")
			switch state {
			case "user":
				audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", user.ID)
			case "template":
				template.Enabled = false
				if _, err := s.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := s.RevokeSubscription(user.ID, sub.ID, sub.Revision); err != nil {
					t.Fatal(err)
				}
			}
			current, err := s.UserSubscription(user.ID, sub.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.UpdateSubscriptionNodes(user.ID, sub.ID, []string{node.ID}, current.Revision); err == nil {
				t.Fatal("disabled/revoked activation succeeded")
			}
			current, err = s.UserSubscription(user.ID, sub.ID)
			if err != nil || len(current.NodeIDs) != 0 || len(current.Periods) != 0 || !current.ActivationPending {
				t.Fatal("failed activation partially committed", current, err)
			}
		})
	}
}

package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRenewalKeepsListedNodeUntilServerActuallyExpires(t *testing.T) {
	s, now, user, plan, sub, _ := renewalFixture(t)
	node, err := s.Node(sub.NodeIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	automaticTestBilling(t, s, node, now.Add(-24*time.Hour), now.Add(time.Second))
	// A historical zero template/target can still own a real assignment.
	// An offline node outside the template pool remains valid for this owner.
	plan.NodesPerSubscription, plan.NodeIDs = 0, nil
	audienceExec(t, s, "UPDATE portal_templates SET config=? WHERE id=?", jsonString(plan), plan.ID)
	audienceExec(t, s, "UPDATE subscription_allocations SET target_count=0 WHERE subscription_id=?", sub.ID)
	audienceExec(t, s, "UPDATE nodes SET last_seen=0 WHERE id=?", node.ID)
	paidRenewal(t, s, plan, sub, "zero-target-live-server")
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || !slices.Equal(after.NodeIDs, sub.NodeIDs) || after.NodesPerSubscription != 0 || !after.ExpiresAt.After(sub.ExpiresAt) {
		t.Fatal("renewal lost an existing node", after, err)
	}
}

func TestRenewalReplacesOnlyExpiredNodesAtReceipt(t *testing.T) {
	s, now, user, plan, _ := paymentFixture(t)
	plan.NodesPerSubscription = 2
	plan, err := s.SaveSubscriptionTemplate(plan.ID, plan, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	sub := assignAllocation(t, s, user.ID, plan)
	keep, lost := sub.NodeIDs[0], sub.NodeIDs[1]
	keepNode, err := s.Node(keep)
	if err != nil {
		t.Fatal(err)
	}
	lostNode, err := s.Node(lost)
	if err != nil {
		t.Fatal(err)
	}
	automaticTestBilling(t, s, keepNode, now.Add(-24*time.Hour), now.Add(time.Hour))
	automaticTestBilling(t, s, lostNode, now.Add(-24*time.Hour), now.Add(time.Second))
	audienceExec(t, s, "UPDATE nodes SET last_seen=0 WHERE id=?", keep)
	password := subscriptionPassword(t, s, user.ID, sub.ID, lost)
	lease, err := s.AcquireLease(lost, strings.Repeat("x", 32), password)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err := s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(plan, *current, "mixed-expiry-renewal"), paymentTestClient())
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second) // Exactly equal to the server expiry.
	for i := 0; i < 2; i++ {
		if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || len(after.Periods) != 2 || len(after.NodeIDs) != 2 || !slices.Contains(after.NodeIDs, keep) || slices.Contains(after.NodeIDs, lost) || slices.Contains(after.NodeIDs, user.Subscriptions[0].NodeIDs[0]) {
		t.Fatal("expected only the expired assignment to change", after, err)
	}
	profile, _, err := s.PortalClientProfile(user.ID, "subscription", sub.ID)
	if err != nil || len(profile.Nodes) != 2 {
		t.Fatal("client did not receive both nodes", err)
	}
	for _, n := range profile.Nodes {
		if !slices.Contains(after.NodeIDs, n.ID) {
			t.Fatal("client exposed an unassigned node", n.ID)
		}
	}
	var revoked bool
	if err = s.DB.QueryRow("SELECT revoked FROM portal_leases WHERE id=?", lease.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatal("expired node lease survived replacement", err)
	}
}

func TestRenewalNodeShortageBlocksUntilRepair(t *testing.T) {
	for _, mode := range []string{"automatic", "custom"} {
		t.Run(mode, func(t *testing.T) {
			s, now, user, plan, sub, _ := renewalFixture(t)
			if mode == "custom" {
				// Activated custom subscriptions stay admin-owned even when their
				// template now has enough automatic stock to replace the node.
				audienceExec(t, s, "INSERT INTO subscription_provisioning(subscription_id,validity_days,activated_at) VALUES(?,?,?)", sub.ID, plan.ValidityDays, sub.CreatedAt.Unix())
				audienceExec(t, s, "UPDATE subscription_allocations SET target_count=0 WHERE subscription_id=?", sub.ID)
			}
			node, err := s.Node(sub.NodeIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			order, err := s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(plan, sub, "expiry-after-checkout"), paymentTestClient())
			if err != nil {
				t.Fatal(err)
			}
			automaticTestBilling(t, s, node, now.Add(-24*time.Hour), now.Add(-time.Second))
			if mode == "automatic" {
				audienceExec(t, s, "UPDATE nodes SET last_seen=0")
			}
			if _, err = s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(plan, sub, "blocked-before-checkout"), paymentTestClient()); !errors.Is(err, ErrRenewalNodes) {
				t.Fatal("expected actionable error before payment", err)
			}
			if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
				t.Fatal(err)
			}
			order, err = s.PaymentOrder(user.ID, order.ID)
			if err != nil || order.PaymentState != "paid" || order.FulfillmentState != "blocked" || !strings.Contains(order.LastError, "管理员") {
				t.Fatal("missing paid order with reason", order, err)
			}
			before, err := s.UserSubscription(user.ID, sub.ID)
			if err != nil || !slices.Equal(before.NodeIDs, sub.NodeIDs) || !before.ExpiresAt.Equal(sub.ExpiresAt) || len(before.Periods) != 1 {
				t.Fatal("blocked receipt partially fulfilled", before, err)
			}
			refreshAllocationReports(t, s)
			var manualNode string
			if mode == "custom" {
				options, err := s.SubscriptionNodes(user.ID, sub.ID)
				if err != nil || options.Subscription.NodesPerSubscription != len(sub.NodeIDs) {
					t.Fatal("admin editor cannot repair zero-count grant", err)
				}
				manualNode = automaticTestNode(t, s, "manual-renewal-replacement").ID
				if _, err = s.UpdateSubscriptionNodes(user.ID, sub.ID, []string{manualNode}, before.Revision); err != nil {
					t.Fatal(err)
				}
			}
			order, err = s.RetryPaymentFulfillment(order.ID, order.Revision, "admin")
			if err != nil || order.FulfillmentState != "fulfilled" {
				t.Fatal("retry did not fulfill original order", order, err)
			}
			after, err := s.UserSubscription(user.ID, sub.ID)
			if err != nil || len(after.NodeIDs) != 1 || after.NodeIDs[0] == node.ID || len(after.Periods) != 2 || manualNode != "" && after.NodeIDs[0] != manualNode {
				t.Fatal("unexpected repaired allocation", after, err)
			}
		})
	}
}

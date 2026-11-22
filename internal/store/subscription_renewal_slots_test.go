package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Use a real granted instance: its origin must not force users to buy another
// copy (and therefore another node) when paying to extend the existing term.
func TestRenewalManualGrantKeepsOriginalSubscription(t *testing.T) {
	s, _, user, template, _ := paymentFixture(t)
	sub, err := s.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if err != nil || !sub.Renewable {
		t.Fatalf("priced active manual grant must be renewable: %+v, %v", sub, err)
	}
	beforeLink, err := s.PortalSubscription(user.ID, sub.ID, "get")
	if err != nil {
		t.Fatal(err)
	}
	order := paidRenewal(t, s, template, *sub, "manual-grant-renewal")
	if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || len(after.Periods) != 2 || !after.ExpiresAt.Equal(sub.ExpiresAt.Add(24*time.Hour)) || !reflect.DeepEqual(after.NodeIDs, sub.NodeIDs) {
		t.Fatal("manual renewal changed allocation or duplicated extension", after, err)
	}
	afterLink, err := s.PortalSubscription(user.ID, sub.ID, "get")
	if err != nil || afterLink != beforeLink {
		t.Fatal("manual renewal changed import credentials", err)
	}
	// Replaying the original admin grant must not undo the paid extension.
	if _, err = s.AssignSubscription(user.ID, template.ID, "fixture-grant", 1, template.Revision); err != nil {
		t.Fatal(err)
	}
	all, err := s.UserSubscriptions(user.ID)
	if err != nil || len(all) != 1 || !all[0].ExpiresAt.Equal(after.ExpiresAt) {
		t.Fatal("manual grant replay changed renewed subscription", all, err)
	}
}

// Payment, node admission and the displayed occupancy use the same persistent
// instance. Exercise both a single assigned node and two nodes from a larger
// pool, with mixed protocols at capacity, repeated receipts and a rollover.
func TestRenewalKeepsAllocationAndConnectionSlots(t *testing.T) {
	for _, per := range []int{1, 2} {
		t.Run(fmt.Sprintf("assigned-%d", per), func(t *testing.T) {
			s, now, user, template, _ := paymentFixture(t)
			template.NodesPerSubscription, template.MaxConnections = per, 3
			template.BandwidthMbps = nil
			var err error
			template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
			if err != nil {
				t.Fatal(err)
			}
			purchase := makePaymentOrder(t, s, user, template, "slots-initial-purchase")
			if err = s.ProcessPaymentNotify(paymentNotice(purchase, "test-payment-secret-v1")); err != nil {
				t.Fatal(err)
			}
			if _, err = s.FulfillPaymentOrders(25); err != nil {
				t.Fatal(err)
			}
			purchase, err = s.PaymentOrder(user.ID, purchase.ID)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := s.UserSubscription(user.ID, purchase.SubscriptionID)
			if err != nil || len(sub.NodeIDs) != per || len(template.NodeIDs) <= per {
				t.Fatal("fixture must assign a subset of the pool", sub, err)
			}
			nodes := append([]string(nil), sub.NodeIDs...)
			passwords := map[string]string{}
			for _, node := range nodes {
				passwords[node] = subscriptionPassword(t, s, user.ID, sub.ID, node)
				if err = s.SetOfflineAdmissionEnabled(node, true); err != nil {
					t.Fatal(err)
				}
			}
			enableBilling(t, s, 100, 100)
			acquire := func(node, protocol, key, ip string) (model.OfflineGrant, error) {
				return s.AcquireNodeQuotaGrant(node, fmt.Sprintf("%032s", key), passwords[node], protocol, ip)
			}
			check := func(want int) *model.UserSubscription {
				t.Helper()
				current, err := s.UserSubscription(user.ID, sub.ID)
				if err != nil || current.Occupied != want || current.MaxConnections != 3 || current.NodesPerSubscription != per || !reflect.DeepEqual(current.NodeIDs, nodes) {
					t.Fatalf("allocation/occupancy changed: want %d/3 on %v, got %+v, %v", want, nodes, current, err)
				}
				return current
			}
			var old []model.OfflineGrant
			for i, protocol := range []string{"hysteria2", "hysteria2", "vless", "vless"} {
				g, err := acquire(nodes[i%per], protocol, fmt.Sprint(i), "198.51.100.1")
				if err != nil {
					t.Fatal(i, err)
				}
				old = append(old, g)
			}
			before := check(3) // two HY2 instances plus one shared VLESS source IP.
			// Changing the default allocation size and removing fresh stock cannot
			// reallocate the existing subscription or prevent its renewal.
			template.NodesPerSubscription = 3
			template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
			if err != nil {
				t.Fatal(err)
			}
			audienceExec(t, s, "UPDATE nodes SET last_seen=0")
			order := paidRenewal(t, s, template, *before, "slots-renewal")
			if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
				t.Fatal(err)
			}
			after := check(3)
			if len(after.Periods) != 2 || !after.CurrentPeriodEndsAt.Equal(before.CurrentPeriodEndsAt) || !after.ExpiresAt.Equal(before.ExpiresAt.Add(24*time.Hour)) {
				t.Fatal("renewal changed current cycle or appended twice", after)
			}
			var reservations int
			if err = s.DB.QueryRow("SELECT COUNT(*) FROM payment_node_reservations WHERE order_id=?", order.ID).Scan(&reservations); err != nil || reservations != 0 {
				t.Fatal("renewal reserved replacement nodes", reservations, err)
			}
			if _, err = acquire(nodes[0], "hysteria2", "full", "198.51.100.1"); !errors.Is(err, ErrSlots) {
				t.Fatal("renewal granted extra device slot", err)
			}
			if _, err = acquire(nodes[0], "vless", "new-ip", "198.51.100.2"); !errors.Is(err, ErrSlots) {
				t.Fatal("renewal bypassed VLESS IP cap", err)
			}
			// Release one HY2 instance, then reuse its slot without allowing a
			// released old grant to silently reclaim the replacement's slot.
			released, err := s.ReportOfflineGrant(old[0].NodeID, old[0].ID, model.OfflineUsage{Version: 1, SlotAction: "release"})
			if err != nil || !released.SlotReleased {
				t.Fatal(released, err)
			}
			check(2)
			replacement, err := acquire(nodes[0], "hysteria2", "replacement", "198.51.100.1")
			if err != nil {
				t.Fatal(err)
			}
			old = append(old, replacement)
			check(3)
			released, err = s.ReportOfflineGrant(old[0].NodeID, old[0].ID, model.OfflineUsage{Version: 2, SlotAction: "acquire"})
			if err != nil || !released.SlotReleased {
				t.Fatal("old grant exceeded full cap after renewal", released, err)
			}
			// Old period permits expire at the original boundary. Late final
			// reports must not free the new period's independently admitted slots.
			*now = before.CurrentPeriodEndsAt
			check(0)
			fresh, err := acquire(nodes[0], "hysteria2", "next-period", "198.51.100.1")
			if err != nil || fresh.NodeQuota.PeriodID == before.CurrentPeriodID {
				t.Fatal("next period not usable on original node", fresh, err)
			}
			check(1)
			for i, g := range old {
				version := int64(1)
				if i == 0 {
					version = 3
				}
				if _, err = s.ReportOfflineGrant(g.NodeID, g.ID, model.OfflineUsage{Version: version, Closed: true, SlotAction: "release"}); err != nil {
					t.Fatal("late final", err)
				}
				check(1)
			}
			if _, err = s.ReportOfflineGrant(fresh.NodeID, fresh.ID, model.OfflineUsage{Version: 1, Closed: true, SlotAction: "release"}); err != nil {
				t.Fatal(err)
			}
			check(0)
			all, err := s.UserSubscriptions(user.ID)
			if err != nil || len(all) != 2 { // fixture grant and the original purchase only.
				t.Fatal("renewal created another subscription", all, err)
			}
			// The same request still resolves to the original fulfilled order.
			replay, err := s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(template, *before, "slots-renewal"), paymentTestClient())
			if err != nil || replay.ID != order.ID {
				t.Fatal("renewal replay changed order", replay, err)
			}
		})
	}
}

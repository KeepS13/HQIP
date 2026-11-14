package store

import (
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestSubscriptionAllocationMigrationPreservesLegacyRightsAndOrders(t *testing.T) {
	s, now, u, p := allocationFixture(t, 6, 2)
	first := assignAllocation(t, s, u.ID, p)
	// Emulate the old schema: template-wide rights, with neither count nor
	// description. An existing paid/unpaid order still owns its old snapshot.
	secret := "test-payment-secret-v1"
	if _, err := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", Secret: &secret, PayType: "aloop", PayDurationMinutes: 10}, "test"); err != nil {
		t.Fatal(err)
	}
	order := makePaymentOrder(t, s, u, p, "legacy-pending-order")
	audienceExec(t, s, "UPDATE portal_templates SET config=json_remove(config,'$.nodesPerSubscription','$.description') WHERE id=?", p.ID)
	audienceExec(t, s, "UPDATE payment_orders SET template_snapshot=json_remove(template_snapshot,'$.nodesPerSubscription','$.description') WHERE id=?", order.ID)
	audienceExec(t, s, "UPDATE portal_usage SET upload=123,download=456 WHERE subscription_id=?", first.ID)
	audienceExec(t, s, "DROP TABLE payment_node_reservations")
	audienceExec(t, s, "DROP TABLE subscription_allocations")
	audienceExec(t, s, "DELETE FROM metadata WHERE key='subscription_allocations_version'")
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateSubscriptionAllocations(tx, *now, s.Postgres()); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.UserSubscription(u.ID, first.ID)
	if err != nil || !sameNodeSet(legacy.NodeIDs, p.NodeIDs) || legacy.NodesPerSubscription != 6 || legacy.UsedBytes != 579 || !legacy.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatal(legacy, err)
	}
	saved, err := s.SubscriptionTemplate(p.ID)
	if err != nil || saved.NodesPerSubscription != 6 || saved.Description != "" {
		t.Fatal(saved, err)
	}
	closed, err := s.PaymentOrder(u.ID, order.ID)
	if err != nil || closed.Status != "closed" {
		t.Fatal("unreservable old order not closed", closed, err)
	}
	var target int
	if err = s.DB.QueryRow("SELECT target_count FROM payment_node_reservations WHERE order_id=?", order.ID).Scan(&target); err != nil || target != 6 {
		t.Fatal(target, err)
	}
	// A second migration does not reapply the template pool over new rights.
	audienceExec(t, s, "UPDATE subscription_allocations SET node_ids=? WHERE subscription_id=?", jsonString(p.NodeIDs[:2]), first.ID)
	tx, err = s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateSubscriptionAllocations(tx, *now, s.Postgres()); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	legacy, err = s.UserSubscription(u.ID, first.ID)
	if err != nil || len(legacy.NodeIDs) != 2 {
		t.Fatal("repeat migration overwrote grant", legacy, err)
	}
}

func TestSubscriptionAllocationMigrationReservesLegacyOrdersInOrder(t *testing.T) {
	s, now, u, p := allocationFixture(t, 6, 2)
	secret := "test-payment-secret-v1"
	if _, err := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", Secret: &secret, PayType: "aloop", PayDurationMinutes: 10}, "test"); err != nil {
		t.Fatal(err)
	}
	a := makePaymentOrder(t, s, u, p, "migration-first")
	*now = now.Add(1e9)
	b := makePaymentOrder(t, s, u, p, "migration-second")
	if err := s.ProcessPaymentNotify(paymentNotice(b, secret)); err != nil {
		t.Fatal(err)
	}
	// Both old orders promised the full six-node pool, so only the earlier one
	// can reserve it; the later paid order must remain visible and blocked.
	for _, id := range []string{a.ID, b.ID} {
		audienceExec(t, s, "UPDATE payment_orders SET template_snapshot=json_remove(template_snapshot,'$.nodesPerSubscription') WHERE id=?", id)
	}
	audienceExec(t, s, "DROP TABLE payment_node_reservations")
	audienceExec(t, s, "DROP TABLE subscription_allocations")
	audienceExec(t, s, "DELETE FROM metadata WHERE key='subscription_allocations_version'")
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateSubscriptionAllocations(tx, *now, s.Postgres()); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	first, err := s.PaymentOrder(u.ID, a.ID)
	if err != nil || first.Status != "pending" {
		t.Fatal(first, err)
	}
	second, err := s.PaymentOrder(u.ID, b.ID)
	if err != nil || second.PaymentState != "paid" || second.FulfillmentState != "blocked" {
		t.Fatal(second, err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT target_count FROM payment_node_reservations WHERE order_id=? AND released=0", a.ID).Scan(&count); err != nil || count != 6 {
		t.Fatal(count, err)
	}
}

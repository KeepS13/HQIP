package store

import (
	"context"
	"github.com/KeepS13/HQIP/internal/model"
	"os"
	"strings"
	"testing"
)

func TestTrafficBillingPostgresAdditiveMigrationPreservesOldLease(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("local PostgreSQL integration required")
	}
	quota := int64(64 << 20)
	store, _, user, _, node, password := portalFixture(t, 1, &quota)
	lease, err := store.AcquireLease(node.ID, strings.Repeat("a", 32), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RenewLease(node.ID, lease.ID, model.LeaseUsage{UploadBytes: 1234, DownloadBytes: 4321}); err != nil {
		t.Fatal(err)
	}
	before, err := store.UserSubscription(user.ID, lease.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture owns an isolated schema. Recreate the pre-upgrade state
	// while keeping the real user, raw lease and outstanding reservation.
	for _, statement := range []string{"DROP TABLE traffic_billing_changes", "DROP TABLE traffic_billing_remainders", "DROP TABLE traffic_lease_billing", "DROP TABLE traffic_billing_settings", "DELETE FROM metadata WHERE key='traffic_billing_version'"} {
		if _, err = store.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err = MigratePostgresTrafficBilling(context.Background(), store.postgresDSN); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := store.TrafficBillingSettings()
	if err != nil || settings.Enabled || settings.Hysteria2 != 150 || settings.VLESS != 150 {
		t.Fatal("migration defaults", settings, err)
	}
	after, err := store.UserSubscription(user.ID, lease.SubscriptionID)
	if err != nil || after.UsedBytes != before.UsedBytes || after.ReservedBytes != before.ReservedBytes {
		t.Fatal("migration changed old ledger", after, err)
	}
	enableBilling(t, store, 1000, 1)
	if _, err = store.RenewLease(node.ID, lease.ID, model.LeaseUsage{UploadBytes: 2234, DownloadBytes: 4321, Closed: true}); err != nil {
		t.Fatal(err)
	}
	after, err = store.UserSubscription(user.ID, lease.SubscriptionID)
	if err != nil || after.UsedBytes != 6555 || after.ReservedBytes != 0 {
		t.Fatal("old grant repriced after migration", after, err)
	}
}

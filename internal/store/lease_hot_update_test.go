package store

import (
	"os"
	"testing"
)

func TestPostgresLeaseExpiryRenewalUsesHeapOnlyUpdate(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	s, _, _, _, node, password := portalFixture(t, 2, nil)
	lease := acquire(t, s, node, password, "a")
	tx, err := s.beginLeaseTransaction()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var before, after int64
	query := "SELECT pg_stat_get_xact_tuples_hot_updated('portal_leases'::regclass)"
	if err = tx.QueryRow(query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE portal_leases SET expires=expires+1 WHERE id=?", lease.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(query).Scan(&after); err != nil || after-before != 1 {
		t.Fatal("renewing a lease rewrote shared index pages instead of using HOT", before, after, err)
	}
	// This optimization must retain a bounded scan over unsettled leases, rather
	// than reading every closed historical connection belonging to a subscriber.
	var definition string
	if err = tx.QueryRow("SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND indexname='portal_lease_open_subscription'").Scan(&definition); err != nil {
		t.Fatal("active-lease index is missing", err)
	}
	var partial bool
	if err = tx.QueryRow("SELECT indpred IS NOT NULL FROM pg_index WHERE indexrelid=(SELECT oid FROM pg_class WHERE relname='portal_lease_open_subscription' AND relnamespace=(SELECT oid FROM pg_namespace WHERE nspname=current_schema()))").Scan(&partial); err != nil || !partial {
		t.Fatal("active-lease index no longer excludes settled history", definition, partial, err)
	}
}

package store

import (
	"os"
	"testing"
)

func TestPostgresLeaseReadsAvoidRelationWidePredicateLocks(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	s, _, user, _, node, password := portalFixture(t, 2, nil)
	acquire(t, s, node, password, "a")
	acquire(t, s, node, password, "b")
	tx, err := s.beginLeaseTransaction()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var isolation, sequential string
	if err = tx.QueryRow("SHOW transaction_isolation").Scan(&isolation); err != nil || isolation != "serializable" {
		t.Fatal("lease transaction isolation changed", isolation, err)
	}
	if err = tx.QueryRow("SHOW enable_seqscan").Scan(&sequential); err != nil || sequential != "off" {
		t.Fatal("lease planner settings missing", sequential, err)
	}
	sub, err := userSubscription(tx, user.ID, user.Subscriptions[0].ID, s.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = rebalancePortalRates(tx, sub.ID, sub, s.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(`SELECT c.relname,l.locktype FROM pg_locks l JOIN pg_class c ON c.oid=l.relation
WHERE l.pid=pg_backend_pid() AND l.mode='SIReadLock' AND c.relname IN ('portal_leases','portal_rates','portal_usage')`)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var relation, kind string
		if err = rows.Scan(&relation, &kind); err != nil {
			t.Fatal(err)
		}
		count++
		t.Logf("predicate relation=%s scope=%s", relation, kind)
		if kind == "relation" {
			t.Errorf("small per-subscription read acquired a whole-table predicate lock: %s", relation)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count == 0 {
		t.Fatal("predicate lock inspection did not observe the lease reads")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// SET LOCAL must not force reporting or other pool users onto index scans.
	if err = s.DB.QueryRow("SHOW enable_seqscan").Scan(&sequential); err != nil || sequential != "on" {
		t.Fatal("lease planner preference leaked outside its transaction", sequential, err)
	}
}

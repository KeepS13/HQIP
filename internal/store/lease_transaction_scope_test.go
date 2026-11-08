package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/pgstore"
)

func TestPostgresLeaseScopeQueuesOnlyMatchingSubscriptionBeforeSnapshot(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	s, _, user, template, _, _ := portalFixture(t, 2, nil)
	other := grantAnother(t, s, user.ID, template, "independent-subscription")
	peer, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	firstID := user.Subscriptions[0].ID
	query := "SELECT id FROM portal_subscriptions WHERE id=?"
	first, err := s.leaseTransactionScope(query, firstID)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	type outcome struct {
		scope *leaseTransactionScope
		err   error
	}
	queued := make(chan outcome, 1)
	go func() {
		scope, err := peer.leaseTransactionScope(query, firstID)
		queued <- outcome{scope, err}
	}()
	select {
	case got := <-queued:
		if got.scope != nil {
			got.scope.close()
		}
		t.Fatal("matching subscription was not queued", got.err)
	case <-time.After(100 * time.Millisecond):
	}
	independent, err := peer.leaseTransactionScope(query, other.ID)
	if err != nil {
		t.Fatal("independent subscription blocked on another subscription", err)
	}
	independent.close()
	tx, err := first.begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE portal_usage SET upload=7 WHERE subscription_id=?", firstID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	first.close()
	var second *leaseTransactionScope
	select {
	case got := <-queued:
		if got.err != nil {
			t.Fatal(got.err)
		}
		second = got.scope
	case <-time.After(5 * time.Second):
		t.Fatal("queued subscription did not resume")
	}
	defer second.close()
	tx, err = second.begin()
	if err != nil {
		t.Fatal(err)
	}
	var upload int64
	if err = tx.QueryRow("SELECT upload FROM portal_usage WHERE subscription_id=?", firstID).Scan(&upload); err != nil || upload != 7 {
		tx.Rollback()
		t.Fatal("queued writer reused a stale Serializable snapshot", upload, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	second.close()
	// A fresh pool cannot reenter a leaked session lock from an earlier owner.
	probe, err := pgstore.Open(s.postgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := probe.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var acquired bool
	key := "hqip:lease-subscription:" + firstID
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtextextended(?,0))", key).Scan(&acquired); err != nil || !acquired {
		t.Fatal("lease subscription lock leaked into the connection pool", acquired, err)
	}
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtextextended(?,0))", key); err != nil {
		t.Fatal(err)
	}
}

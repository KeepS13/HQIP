package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// A committed grant survives a failure while constructing its HTTP response.
// Retrying the original key, with its now-stale revisions, recovers one grant
// and must not duplicate the allocation, period, usage ledger or audit entry.
func TestPostgresManualAssignmentRecoversAfterResponseReadFailure(t *testing.T) {
	s, _, user, template := allocationFixture(t, 2, 1)
	if !s.Postgres() {
		t.Skip("PostgreSQL integration environment not configured")
	}
	installLeaseReadFault(t, s, "portal_users", "SELECT s.id,s.user_id", "57014")
	_, err := s.AssignSubscription(user.ID, template.ID, "lost-grant-response", user.Revision, template.Revision)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "57014" || !errors.Is(err, ErrSubscriptionAssignmentUnconfirmed) {
		t.Fatal("response failure lost its recovery semantics or database cause", err)
	}
	var committed string
	if err = s.DB.QueryRow("SELECT id FROM portal_subscriptions WHERE user_id=? AND grant_key=?", user.ID, "admin:lost-grant-response").Scan(&committed); err != nil {
		t.Fatal("response rendering rolled back an otherwise complete grant", err)
	}
	for i := 0; i < 2; i++ {
		recovered, err := s.AssignSubscription(user.ID, template.ID, "lost-grant-response", user.Revision, template.Revision)
		if err != nil || recovered.ID != committed || len(recovered.NodeIDs) != 1 {
			t.Fatal("replay did not recover the original allocation", recovered, err)
		}
	}
	for _, table := range []string{"portal_subscriptions", "subscription_allocations", "portal_usage", "subscription_periods"} {
		var count int
		if err = s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatal("replay duplicated grant data", table, count, err)
		}
	}
	var count, revision int64
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='subscription.assigned' AND subject=?", committed).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay duplicated audit", count, err)
	}
	if err = s.DB.QueryRow("SELECT revision FROM portal_users WHERE id=?", user.ID).Scan(&revision); err != nil || revision != user.Revision+1 {
		t.Fatal("replay mutated the user revision", revision, err)
	}
}

// An unrelated subscription keeps reporting while an administrator grants a
// new one. Neither operation changes the other's business data; broad predicate
// locks on empty/new ledgers must not turn this into a serialization cycle.
func TestPostgresManualAssignmentDuringUnrelatedUsage(t *testing.T) {
	s, _, first, template, node, password := portalFixture(t, 2, nil)
	if !s.Postgres() {
		t.Skip("PostgreSQL integration environment not configured")
	}
	lease := acquire(t, s, node, password, "a")
	user, err := s.RegisterPortal("manual-concurrent@example.com", "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgstore.Config(s.postgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	raw := stdlib.OpenDB(*config)
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gate, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	const lock = 827360119
	if _, err = gate.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lock); err != nil {
		t.Fatal(err)
	}
	defer gate.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", lock)
	// Pause after all grant reads/writes, before commit, without sleeping inside
	// production code. A PostgreSQL trigger exists only in this test's schema.
	for _, statement := range []string{
		fmt.Sprintf(`CREATE FUNCTION pause_assignment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.assigned' THEN PERFORM pg_advisory_xact_lock(%d); END IF; RETURN NEW; END $$`, lock),
		`CREATE TRIGGER pause_assignment BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION pause_assignment()`,
	} {
		if _, err = raw.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.beginLeaseTransaction()
	if err != nil {
		t.Fatal(err)
	}
	defer report.Rollback()
	var revision int64
	if err = report.QueryRow("SELECT revision FROM portal_users WHERE id=?", user.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		value model.UserSubscription
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := s.AssignSubscription(user.ID, template.ID, "manual-under-usage", user.Revision, template.Revision)
		done <- outcome{value, err}
	}()
	defer func() {
		gate.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", lock)
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("assignment did not stop")
		}
	}()
	for {
		var waiting bool
		err = raw.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND NOT granted)", lock).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = report.Exec("UPDATE portal_leases SET upload=upload+1 WHERE id=?", lease.ID); err != nil {
		t.Fatalf("manual grant conflicted with unrelated live usage: %v", err)
	}
	if err = report.Commit(); err != nil {
		t.Fatalf("unrelated usage could not commit during manual grant: %v", err)
	}
	if _, err = gate.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lock); err != nil {
		t.Fatal(err)
	}
	result := <-done
	done <- result // allow the cleanup to join the completed goroutine
	if result.err != nil || result.value.ID == "" {
		t.Fatalf("manual grant failed: %v", result.err)
	}
	var used int64
	if err = s.DB.QueryRow("SELECT upload FROM portal_leases WHERE id=? AND user_id=?", lease.ID, first.ID).Scan(&used); err != nil || used != 1 {
		t.Fatal("live usage was lost", used, err)
	}
	replayed, err := s.AssignSubscription(user.ID, template.ID, "manual-under-usage", user.Revision, template.Revision)
	if err != nil || replayed.ID != result.value.ID {
		t.Fatal("lost-response replay did not return the same grant", err)
	}
}

// Allocation uses allocated user counts, not live online identities. Blocking
// the unrelated identity table must not block stock selection or a gift.
func TestPostgresAllocationDoesNotReadOnlineIdentities(t *testing.T) {
	s, _, user, template := allocationFixture(t, 2, 1)
	if !s.Postgres() {
		t.Skip("PostgreSQL integration environment not configured")
	}
	blocker, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.Exec("LOCK TABLE node_accounting_identities IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("SET LOCAL lock_timeout='100ms'"); err != nil {
		t.Fatal(err)
	}
	ids, err := allocateSubscriptionNodes(tx, user.ID, template, s.Now())
	if err != nil || len(ids) != 1 {
		t.Fatalf("allocation unnecessarily depended on live identities: %v", err)
	}
}

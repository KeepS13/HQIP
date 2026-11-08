package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// A view on this test's isolated schema raises a real PostgreSQL error on the
// selected read. Sequences are not rolled back, so the retry can succeed. This
// reproduces an aborted transaction rather than merely returning a mock error:
// continuing SQL after the injected failure produces 25P02 and loses its cause.
func installLeaseReadFault(t *testing.T, s *Store, table, queryPart, code string) *sql.DB {
	t.Helper()
	config, err := pgstore.Config(s.postgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	raw := stdlib.OpenDB(*config)
	t.Cleanup(func() { raw.Close() })
	// All identifiers and SQLSTATE values below are fixed test-case literals.
	statements := []string{
		"CREATE SEQUENCE lease_read_fault_calls",
		fmt.Sprintf(`CREATE FUNCTION lease_read_fault() RETURNS boolean LANGUAGE plpgsql AS $$
BEGIN
 IF strpos(current_query(), '%s') > 0 THEN
  IF nextval('lease_read_fault_calls') = 1 THEN
   RAISE EXCEPTION 'injected lease read failure' USING ERRCODE = '%s';
  END IF;
 END IF;
 RETURN true;
END $$`, strings.ReplaceAll(queryPart, "'", "''"), code),
		"ALTER TABLE " + table + " RENAME TO lease_read_fault_source",
		"CREATE VIEW " + table + " AS SELECT * FROM lease_read_fault_source WHERE lease_read_fault()",
	}
	for _, statement := range statements {
		if _, err = raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		var called bool
		if err := raw.QueryRow("SELECT is_called FROM lease_read_fault_calls").Scan(&called); err != nil || !called {
			t.Error("the intended PostgreSQL read failure was not exercised", called, err)
		}
	})
	return raw
}

func TestPostgresLeaseReadFailuresPreserveTransactionCause(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	for _, stage := range []struct {
		name, table, queryPart string
	}{
		{"subscription", "portal_users", "SELECT s.id,s.user_id"},
		{"session", "portal_subscriptions", "SELECT COUNT(*) FROM portal_subscriptions"},
	} {
		for _, code := range []string{"40001", "40P01", "57014"} {
			t.Run(stage.name+"/"+code, func(t *testing.T) {
				quota := int64(64 << 20)
				s, _, user, _, node, password := portalFixture(t, 2, &quota)
				lease := acquire(t, s, node, password, "a")
				raw := installLeaseReadFault(t, s, stage.table, stage.queryPart, code)
				usage := model.LeaseUsage{UploadBytes: 123, DownloadBytes: 456, AppliedRateVersion: lease.RateVersion}
				updated, err := s.RenewLease(node.ID, lease.ID, usage)
				if code == "57014" {
					var pg *pgconn.PgError
					if !errors.As(err, &pg) || pg.Code != code {
						t.Fatalf("database failure was replaced by authorization/aborted-transaction error: %v", err)
					}
					var up, down, reserved int64
					var revoked bool
					if err = raw.QueryRow("SELECT upload,download,reserved FROM portal_usage WHERE subscription_id=$1", lease.SubscriptionID).Scan(&up, &down, &reserved); err != nil || up != 0 || down != 0 || reserved != lease.BudgetBytes {
						t.Fatalf("failed renewal changed the accounting ledger: up=%d down=%d reserved=%d err=%v", up, down, reserved, err)
					}
					if err = raw.QueryRow("SELECT revoked FROM portal_leases WHERE id=$1", lease.ID).Scan(&revoked); err != nil || revoked {
						t.Fatal("database read failure revoked a valid lease", revoked, err)
					}
					updated, err = s.RenewLease(node.ID, lease.ID, usage)
				}
				if err != nil || updated.Closed || updated.ConsumedBytes != 579 {
					t.Fatalf("renewal did not recover with exact accounting: consumed=%d err=%v", updated.ConsumedBytes, err)
				}
				usage.Closed = true
				usage.AppliedRateVersion = updated.RateVersion
				for i := 0; i < 2; i++ {
					if _, err = s.RenewLease(node.ID, lease.ID, usage); err != nil {
						t.Fatal("final receipt/replay failed", err)
					}
				}
				sub, err := s.UserSubscription(user.ID, lease.SubscriptionID)
				if err != nil || sub.UsedBytes != 579 || sub.ReservedBytes != 0 || sub.Occupied != 0 {
					t.Fatalf("retry/replay double-charged or retained reservations: subscription=%+v err=%v", sub, err)
				}
			})
		}
	}
}

func TestPostgresAdmissionReadFailuresAreRetried(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	for _, stage := range []struct {
		name, table, queryPart string
	}{
		{"portal_enabled", "metadata", "key='portal_enabled'"},
		{"recovery_hold", "metadata", "key='portal_recovery_hold'"},
		{"credential", "portal_credentials", "SELECT user_id,scope,subscription_id"},
		{"session", "portal_subscriptions", "SELECT COUNT(*) FROM portal_subscriptions"},
		{"node_ready", "nodes", "SELECT COUNT(*) FROM nodes"},
	} {
		t.Run(stage.name, func(t *testing.T) {
			quota := int64(64 << 20)
			s, _, user, _, node, password := portalFixture(t, 2, &quota)
			if _, err := s.DB.Exec("INSERT INTO metadata(key,value) VALUES('portal_recovery_hold','false') ON CONFLICT(key) DO UPDATE SET value=excluded.value"); err != nil {
				t.Fatal(err)
			}
			installLeaseReadFault(t, s, stage.table, stage.queryPart, "40001")
			lease, err := s.AcquireLease(node.ID, strings.Repeat("a", 32), password)
			if err != nil {
				t.Fatal("retryable admission read was treated as an authorization failure", err)
			}
			if _, err = s.RenewLease(node.ID, lease.ID, model.LeaseUsage{Closed: true, AppliedRateVersion: lease.RateVersion}); err != nil {
				t.Fatal(err)
			}
			sub, err := s.UserSubscription(user.ID, lease.SubscriptionID)
			if err != nil || sub.UsedBytes != 0 || sub.ReservedBytes != 0 || sub.Occupied != 0 {
				t.Fatal("admission retry left a duplicate lease or reservation", sub, err)
			}
		})
	}
}

func TestPostgresCredentialReadFailureRemainsDatabaseError(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	s, _, user, _, node, password := portalFixture(t, 2, nil)
	installLeaseReadFault(t, s, "portal_subscriptions", "SELECT COUNT(*) FROM portal_subscriptions", "57014")
	_, err := s.PortalCredential(user.ID, user.Subscriptions[0].ID, node.ID, "subscription")
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "57014" {
		t.Fatal("credential query failure was replaced by an authorization denial", err)
	}
	got, err := s.PortalCredential(user.ID, user.Subscriptions[0].ID, node.ID, "subscription")
	if err != nil || got != password {
		t.Fatal("credential changed after a transient database error", err)
	}
}

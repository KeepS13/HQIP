package store

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresOperationsMigrationPreservesLoginAndIsIdempotent(t *testing.T) {
	s, _, token := nativeRefreshFixture(t)
	if !s.Postgres() {
		t.Skip("PostgreSQL integration")
	}
	for _, query := range []string{"DROP TABLE portal_refresh_receipts", "DROP TABLE server_operations", "DROP TABLE server_credentials", "DELETE FROM metadata WHERE key='operations_schema_version'"} {
		if _, err := s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := MigratePostgresOperations(context.Background(), s.postgresDSN); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := s.PortalIdentity(token.AccessToken, "native"); err != nil {
		t.Fatal("migration invalidated login", err)
	}
	fresh, err := s.PortalRefreshRequest(token.RefreshToken, strings.Repeat("a", 48))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Now = s.Now
	if _, _, _, err = reopened.PortalIdentity(fresh.AccessToken, "native"); err != nil {
		t.Fatal("restart invalidated login", err)
	}
	if err = reopened.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedPasswordCompletionRetryDoesNotRotateTwice(t *testing.T) {
	s, _, v, _ := managedServerFixture(t)
	if _, err := s.QueueServerOperation(v.ID, "", "password", "new-password-123", "", v.Revision); err != nil {
		t.Fatal(err)
	}
	j, err := s.NextServerOperation()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.FinishServerOperation(j, "succeeded", "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.ServerCredential(v.ID)
	if err != nil || c.Revision != 2 {
		t.Fatalf("rotation repeated: revision=%d err=%v", c.Revision, err)
	}
}

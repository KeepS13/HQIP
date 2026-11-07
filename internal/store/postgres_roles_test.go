package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/secure"
)

// This test uses the generated Compose configuration in a disposable CI
// database, including the production role grants, rather than a superuser.
func TestPostgresApplicationRole(t *testing.T) {
	migration := os.Getenv("HQIP_TEST_POSTGRES_MIGRATION_URL")
	app := os.Getenv("HQIP_TEST_POSTGRES_APP_URL")
	if migration == "" || app == "" {
		t.Skip("generated storage stack not configured")
	}
	for _, dsn := range []string{migration, app} {
		c, err := pgstore.Config(dsn)
		if err != nil || c.Host != "127.0.0.1" || c.Database != "hqip" {
			t.Fatal("role tests require explicit local disposable storage")
		}
	}
	if err := InitializePostgres(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := secure.Open(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	s, err := OpenPostgres(dir, "https://test.example", app)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var canCreate, superuser bool
	if err = s.DB.QueryRow("SELECT has_schema_privilege(current_user,'public','CREATE')").Scan(&canCreate); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow("SELECT rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&superuser); err != nil || canCreate || superuser {
		t.Fatal("application role can change schema or escalate", err)
	}
	if err = s.InitAdmin("test-admin", "isolated-role-password"); err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateNode(model.Node{Name: "role-test", SSHHost: "test.example", Domain: "test.example", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Event(n.ID, "test", "application role insert"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Node(n.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
}

package store

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/jackc/pgx/v5/stdlib"
)

// Each test owns an isolated schema; no real account or production DB is used.
func postgresFixture(t *testing.T, sqlite *Store) *Store {
	t.Helper()
	dsn := emptyPostgresSchema(t)
	if err := InitializePostgres(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	dir, base := sqlite.DataDir, sqlite.BaseURL
	sqlite.Close()
	s, err := OpenPostgres(dir, base, dsn)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func emptyPostgresSchema(t *testing.T) string {
	t.Helper()
	source := os.Getenv("HQIP_TEST_POSTGRES_URL")
	if source == "" {
		t.Skip("set HQIP_TEST_POSTGRES_URL to run PostgreSQL integration tests")
	}
	u, e := url.Parse(source)
	if e != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("PostgreSQL tests require explicit local 127.0.0.1 URL")
	}
	c, e := pgstore.Config(source)
	if e != nil {
		t.Fatal(e)
	}
	raw := stdlib.OpenDB(*c)
	schema := "test_" + time.Now().Format("150405000000000") + "_" + NewID()
	// NewID can include '-' and uppercase; quoting is required and no user input
	// enters this identifier. Cleanup uses the same generated name only.
	name := `"` + schema + `"`
	if _, e = raw.Exec("CREATE SCHEMA " + name); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { raw.Exec("DROP SCHEMA " + name + " CASCADE"); raw.Close() })
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String()
}

func reopenFixture(s *Store) (*Store, error) {
	if s.postgresDSN != "" {
		return OpenPostgres(s.DataDir, s.BaseURL, s.postgresDSN)
	}
	return Open(s.DataDir, s.BaseURL)
}

package control

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
	"github.com/jackc/pgx/v5/stdlib"
)

func controlTestStore(t *testing.T) (*store.Store, error) {
	t.Helper()
	dir := t.TempDir()
	dsn := os.Getenv("HQIP_TEST_POSTGRES_URL")
	if dsn == "" {
		return store.Open(dir, "https://panel.example.com")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("tests require local PostgreSQL")
	}
	c, err := pgstore.Config(dsn)
	if err != nil {
		return nil, err
	}
	raw := stdlib.OpenDB(*c)
	name := `"control_test_` + store.NewID() + `"`
	if _, err = raw.Exec("CREATE SCHEMA " + name); err != nil {
		raw.Close()
		return nil, err
	}
	t.Cleanup(func() { raw.Exec("DROP SCHEMA " + name + " CASCADE"); raw.Close() })
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	if err = store.InitializePostgres(context.Background(), u.String()); err != nil {
		return nil, err
	}
	if _, err = secure.Open(filepath.Join(dir, "master.key")); err != nil {
		return nil, err
	}
	return store.OpenPostgres(dir, "https://panel.example.com", u.String())
}

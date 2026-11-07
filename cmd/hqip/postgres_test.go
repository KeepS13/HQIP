package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/store"
	"github.com/jackc/pgx/v5/stdlib"
)

func testDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("HQIP_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("tests require a local PostgreSQL server")
	}
	c, err := pgstore.Config(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*c)
	name := fmt.Sprintf("hqip_backup_test_%d", time.Now().UnixNano())
	if _, err = db.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); db.Close() })
	u.Path = "/" + name
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}

func TestPostgresBackupRestoreRoundTrip(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	if runtime.GOOS == "linux" {
		// Match the deployer's sanitized environment, not the CI runner's
		// extended PATH that could hide missing backup-tool resolution.
		t.Setenv("PATH", "/usr/bin:/bin")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("integration requires %s on PATH", tool)
		}
	}
	source := testDatabase(t)
	target := testDatabase(t)
	dir := t.TempDir()
	t.Setenv("HQIP_DATABASE_URL", source)
	if err := initAdmin([]string{"--data-dir", dir, "--output", filepath.Join(t.TempDir(), "admin.json"), "--initialize-postgres"}); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenPostgres(dir, "https://test.example", source)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateNode(model.Node{Name: "restored", SSHHost: "restored.example", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.8", Domain: "restored.example", Port: 443}, "original-password")
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err = backup([]string{"--data-dir", dir, "--output", archive}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HQIP_DATABASE_URL", target)
	restored := t.TempDir()
	if err = restore([]string{"--data-dir", restored, "--input", archive}); err != nil {
		t.Fatal(err)
	}
	s, err = store.OpenPostgres(restored, "https://test.example", target)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Node(n.ID)
	if err != nil || loaded.ID != n.ID {
		t.Fatal("node lost in restore", err)
	}
	if s.PortalSetting("portal_recovery_hold") != "true" {
		t.Fatal("restore skipped ledger recovery hold")
	}
	if err = s.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = restore([]string{"--data-dir", t.TempDir(), "--input", archive}); err == nil {
		t.Fatal("restore overwrote populated database")
	}
}

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/store"
)

func TestPostgresLegacyBackupAddsOperationLogsOnRestore(t *testing.T) {
	if os.Getenv("HQIP_TEST_POSTGRES_URL") == "" {
		t.Skip("PostgreSQL integration environment not configured")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("integration requires %s on PATH", tool)
		}
	}
	source, target := testDatabase(t), testDatabase(t)
	dir := t.TempDir()
	t.Setenv("HQIP_DATABASE_URL", source)
	if err := initAdmin([]string{"--data-dir", dir, "--output", filepath.Join(t.TempDir(), "initial.json"), "--initialize-postgres"}); err != nil {
		t.Fatal(err)
	}
	db, err := pgstore.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"DROP TABLE operation_logs", "DROP TABLE operation_job_context", "DELETE FROM metadata WHERE key='operation_logs_version'", "INSERT INTO audit(at,action,subject) VALUES(1,'legacy.test','preserved')"} {
		if _, err = db.Exec(query); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	dump := filepath.Join(t.TempDir(), "legacy.dump")
	if err = pgstore.Dump(context.Background(), source, dump); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "legacy.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gz)
	for name, path := range map[string]string{"hqip.pg.dump": dump, "master.key": filepath.Join(dir, "master.key")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err = tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for _, close := range []func() error{tarWriter.Close, gz.Close, f.Close} {
		if err = close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HQIP_DATABASE_URL", target)
	restored := t.TempDir()
	if err = restore([]string{"--data-dir", restored, "--input", archive}); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenPostgres(restored, "", target)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version string
	var retained int
	if err = s.DB.QueryRow("SELECT value FROM metadata WHERE key='operation_logs_version'").Scan(&version); err != nil || version != "1" {
		t.Fatal("additive schema not restored", version, err)
	}
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='legacy.test'").Scan(&retained); err != nil || retained != 1 {
		t.Fatal("legacy audit was not preserved", retained, err)
	}
}

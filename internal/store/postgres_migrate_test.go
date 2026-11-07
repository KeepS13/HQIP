package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresMigrationPreservesSecretsAndOpenLedger(t *testing.T) {
	dsn := emptyPostgresSchema(t)
	s, now := sqliteFixture(t)
	n := newNode(t, s, "migrated-node")
	if _, err := s.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]' WHERE id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(n.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	u, err := s.RegisterPortal("migration@example.com", "migration-password")
	if err != nil {
		t.Fatal(err)
	}
	quota := int64(20 << 20)
	p, err := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "Original", Enabled: true, Listed: true, ValidityDays: 30, NodeIDs: []string{n.ID}, DefaultNodeID: n.ID, QuotaBytes: &quota, MaxConnections: 2}, 0)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.AssignSubscription(u.ID, p.ID, "original-grant", 1, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PortalSubscription(u.ID, sub.ID, "create"); err != nil {
		t.Fatal(err)
	}
	password, err := s.PortalCredential(u.ID, sub.ID, n.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	lease := acquire(t, s, n, password, "a")
	lease = renew(t, s, lease, 1234, 4321, false)
	before, err := s.UserSubscription(u.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A prior event ID tests both ID preservation and identity reseeding.
	if err = s.Event(n.ID, "test", "migration event"); err != nil {
		t.Fatal(err)
	}
	var eventID int64
	if err = s.DB.QueryRow("SELECT max(id) FROM events").Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	snapshot := t.TempDir()
	if err = s.Snapshot(context.Background(), filepath.Join(snapshot, "hqip.db")); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(s.DataDir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(snapshot, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "migrated")
	r, err := MigratePostgres(context.Background(), snapshot, target, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tables) != 56 {
		t.Fatalf("unexpected table inventory: %d", len(r.Tables))
	}
	pg, err := OpenPostgres(target, s.BaseURL, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	pg.Now = func() time.Time { return *now }
	if err = pg.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
	after, err := pg.UserSubscription(u.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.UsedBytes != after.UsedBytes || before.ReservedBytes != after.ReservedBytes || before.Occupied != after.Occupied || before.ID != after.ID {
		t.Fatal("migration changed open accounting")
	}
	actual, err := pg.PortalCredential(u.ID, sub.ID, n.ID, "subscription")
	if err != nil || actual != password {
		t.Fatal("credential changed", err)
	}
	if _, err = pg.RenewLease(n.ID, lease.ID, model.LeaseUsage{UploadBytes: 1234, DownloadBytes: 4321, Closed: true, AppliedRateVersion: lease.RateVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.RenewLease(n.ID, lease.ID, model.LeaseUsage{UploadBytes: 1234, DownloadBytes: 4321, Closed: true, AppliedRateVersion: lease.RateVersion}); err != nil {
		t.Fatal(err)
	}
	final, err := pg.UserSubscription(u.ID, sub.ID)
	if err != nil || final.UsedBytes != 5555 || final.ReservedBytes != 0 {
		t.Fatal("settlement changed across migration", final, err)
	}
	if err = pg.Event(n.ID, "test", "new event"); err != nil {
		t.Fatal(err)
	}
	var nextID int64
	if err = pg.DB.QueryRow("SELECT max(id) FROM events").Scan(&nextID); err != nil || nextID <= eventID {
		t.Fatal("identity sequence not restored", err)
	}
	if _, err = Open(target, ""); err == nil {
		t.Fatal("PostgreSQL directory silently fell back to SQLite")
	}
	if _, err = MigratePostgres(context.Background(), snapshot, filepath.Join(t.TempDir(), "repeat"), dsn); err == nil {
		t.Fatal("nonempty target overwritten")
	}
	wrong := t.TempDir()
	if _, err = secure.Open(filepath.Join(wrong, "master.key")); err != nil {
		t.Fatal(err)
	}
	if db, err := OpenPostgres(wrong, "", dsn); err == nil {
		db.Close()
		t.Fatal("wrong vault key accepted")
	}
}

func TestPostgresMigrationFailureRollsBackAllTables(t *testing.T) {
	dsn := emptyPostgresSchema(t)
	s, _ := sqliteFixture(t)
	if _, err := s.DB.Exec("CREATE TABLE unsupported_history(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := MigratePostgres(context.Background(), s.DataDir, filepath.Join(t.TempDir(), "failed"), dsn); err == nil {
		t.Fatal("unknown history silently discarded")
	}
	c, err := pgstore.Config(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*c)
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial migration committed", count, err)
	}
}

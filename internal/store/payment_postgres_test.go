package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/jackc/pgx/v5/stdlib"
)

func assertPaymentMigrationInventory(t *testing.T, s *Store, report MigrationReport) {
	t.Helper()
	rows, e := s.DB.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if e != nil {
		t.Fatal(e)
	}
	expected := map[string]bool{}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			rows.Close()
			t.Fatal(e)
		}
		expected[name] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Tables) != len(expected) {
		t.Fatalf("migration table count: got %d, source has %d", len(report.Tables), len(expected))
	}
	seen := map[string]bool{}
	for _, entry := range report.Tables {
		if !expected[entry.Name] || seen[entry.Name] || len(entry.SHA256) != 64 {
			t.Fatalf("invalid migration inventory entry %+v", entry)
		}
		seen[entry.Name] = true
		var count int64
		if e = s.DB.QueryRow("SELECT COUNT(*) FROM " + quotedIdentifier(entry.Name)).Scan(&count); e != nil || entry.Rows != count {
			t.Fatal("migration row count mismatch", entry.Name, count, entry.Rows, e)
		}
	}
	for _, name := range []string{"payment_settings", "payment_config_versions", "payment_orders", "payment_activities"} {
		if !seen[name] {
			t.Fatal("payment history omitted from migration", name)
		}
	}
}

func TestPostgresPaymentsExplicitMigrationFromLegacySchemaIsIdempotent(t *testing.T) {
	dsn := emptyPostgresSchema(t)
	config, e := pgstore.Config(dsn)
	if e != nil {
		t.Fatal(e)
	}
	raw := stdlib.OpenDB(*config)
	defer raw.Close()
	// Intentionally bootstrap only the pre-payment schema. New runtime startup
	// must refuse it, and only the explicit DDL migration may add payment tables.
	for _, statement := range strings.Split(pgstore.Schema, ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			if _, e = raw.Exec(statement); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e = raw.Exec("INSERT INTO portal_users(id,email,password,created_at) VALUES('legacy-user','legacy@example.com','hash',1)"); e != nil {
		t.Fatal(e)
	}
	template := model.SubscriptionTemplate{ID: "legacy-template", Name: "legacy", Enabled: true, Listed: true, ValidityDays: 30, MaxConnections: 1, Revision: 1}
	if _, e = raw.Exec("INSERT INTO portal_templates(id,config) VALUES($1,$2)", template.ID, jsonString(template)); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec("INSERT INTO portal_requests(id,user_id,template_id,status,requested_at,request_key) VALUES('legacy-request','legacy-user','legacy-template','pending',1,'legacy-request-key')"); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if _, e = secure.Open(filepath.Join(dir, "master.key")); e != nil {
		t.Fatal(e)
	}
	if old, e := OpenPostgres(dir, "https://api.example.com", dsn); e == nil {
		old.Close()
		t.Fatal("runtime attempted implicit payment DDL")
	}
	if e = MigratePostgresPayments(context.Background(), dsn); e != nil {
		t.Fatal(e)
	}
	if e = MigratePostgresSubscriptionAllocations(context.Background(), dsn); e != nil {
		t.Fatal(e)
	}
	if e = MigratePostgresOperations(context.Background(), dsn); e != nil {
		t.Fatal(e)
	}
	if e = MigratePostgresOperationLogs(context.Background(), dsn); e != nil {
		t.Fatal(e)
	}
	if e = MigratePostgresOffline(context.Background(), dsn); e != nil {
		t.Fatal(e)
	}
	s, e := OpenPostgres(dir, "https://api.example.com", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var status, note string
	var decided, revision int64
	if e = s.DB.QueryRow("SELECT status,note,decided_at,revision FROM portal_requests WHERE id='legacy-request'").Scan(&status, &note, &decided, &revision); e != nil || status != "cancelled" || note == "" || decided <= 0 || revision != 2 {
		t.Fatal("legacy request not retired", status, note, decided, revision, e)
	}
	secret := "explicit-migration-secret"
	saved, e := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", PayType: "aloop", PayDurationMinutes: 10, Secret: &secret}, "test-admin")
	if e != nil {
		t.Fatal(e)
	}
	var cipherBefore string
	if e = s.DB.QueryRow("SELECT secret FROM payment_config_versions WHERE version=?", saved.Revision).Scan(&cipherBefore); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec("INSERT INTO portal_requests(id,user_id,template_id,status,requested_at,request_key) VALUES('post-migration-request','legacy-user','legacy-template','pending',2,'new-request-key')"); e != nil {
		t.Fatal(e)
	}
	if e = MigratePostgresPayments(context.Background(), dsn); e != nil {
		t.Fatal(e)
	}
	after, e := s.PaymentSettings()
	if e != nil || after.Revision != saved.Revision || after.MerchantNum != saved.MerchantNum || !after.Enabled {
		t.Fatal("repeat migration changed payment settings", after, e)
	}
	var cipherAfter string
	if e = s.DB.QueryRow("SELECT secret FROM payment_config_versions WHERE version=?", saved.Revision).Scan(&cipherAfter); e != nil || cipherAfter != cipherBefore {
		t.Fatal("repeat migration replaced key", e)
	}
	if e = s.DB.QueryRow("SELECT status FROM portal_requests WHERE id='post-migration-request'").Scan(&status); e != nil || status != "pending" {
		t.Fatal("repeat migration reapplied retirement", status, e)
	}
	var again int64
	if e = s.DB.QueryRow("SELECT decided_at FROM portal_requests WHERE id='legacy-request'").Scan(&again); e != nil || again != decided {
		t.Fatal("repeat migration changed historical decision", again, e)
	}
}

func sqlitePaymentSnapshotFixture(t *testing.T) (*Store, *time.Time, model.PortalUser, model.SubscriptionTemplate, model.PaymentOrder) {
	t.Helper()
	s, now := sqliteFixture(t)
	n := newNode(t, s, "payment-snapshot")
	if _, e := s.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]' WHERE id=?", n.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.SetStatus(n.ID, "ready", "test", false); e != nil {
		t.Fatal(e)
	}
	u, e := s.RegisterPortal("payment-snapshot@example.com", "payment-password")
	if e != nil {
		t.Fatal(e)
	}
	price := int64(1200)
	p, e := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "snapshot subscription", Enabled: true, Listed: true, ValidityDays: 30, MaxConnections: 2, NodeIDs: []string{n.ID}, DefaultNodeID: n.ID, PriceCents: &price}, 0)
	if e != nil {
		t.Fatal(e)
	}
	secret := "test-payment-secret-v1"
	if _, e = s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", PayType: "aloop", PayDurationMinutes: 10, Secret: &secret}, "admin"); e != nil {
		t.Fatal(e)
	}
	o := makePaymentOrder(t, s, u, p, "snapshot-payment-order")
	return s, now, u, p, o
}

func paymentSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	snapshot := t.TempDir()
	if e := s.Snapshot(context.Background(), filepath.Join(snapshot, "hqip.db")); e != nil {
		t.Fatal(e)
	}
	key, e := os.ReadFile(filepath.Join(s.DataDir, "master.key"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(snapshot, "master.key"), key, 0600); e != nil {
		t.Fatal(e)
	}
	return snapshot
}

func TestPostgresPaymentMigrationPreservesEncryptedHistoryAndPendingFulfillment(t *testing.T) {
	dsn := emptyPostgresSchema(t)
	s, now, u, _, o := sqlitePaymentSnapshotFixture(t)
	if e := s.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); e != nil {
		t.Fatal(e)
	}
	var ciphertext, urlCipher string
	if e := s.DB.QueryRow("SELECT secret FROM payment_config_versions WHERE version=1").Scan(&ciphertext); e != nil {
		t.Fatal(e)
	}
	if e := s.DB.QueryRow("SELECT pay_url FROM payment_orders WHERE id=?", o.ID).Scan(&urlCipher); e != nil {
		t.Fatal(e)
	}
	snapshot := paymentSnapshot(t, s)
	target := filepath.Join(t.TempDir(), "postgres-payment")
	report, e := MigratePostgres(context.Background(), snapshot, target, dsn)
	if e != nil {
		t.Fatal(e)
	}
	assertPaymentMigrationInventory(t, s, report)
	key, _ := os.ReadFile(filepath.Join(snapshot, "master.key"))
	digest := sha256.Sum256(key)
	if report.MasterSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("snapshot key identity changed")
	}
	pg, e := OpenPostgres(target, s.BaseURL, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pg.Close()
	pg.Now = func() time.Time { return *now }
	if e = pg.CheckIntegrity(); e != nil {
		t.Fatal(e)
	}
	var gotSecret, gotURL string
	if e = pg.DB.QueryRow("SELECT secret FROM payment_config_versions WHERE version=1").Scan(&gotSecret); e != nil || gotSecret != ciphertext {
		t.Fatal("payment config ciphertext changed", e)
	}
	if e = pg.DB.QueryRow("SELECT pay_url FROM payment_orders WHERE id=?", o.ID).Scan(&gotURL); e != nil || gotURL != urlCipher {
		t.Fatal("payment URL ciphertext changed", e)
	}
	plain, e := pg.Vault.Decrypt(gotURL)
	if e != nil || plain != o.PayURL {
		t.Fatal("payment URL did not decrypt after migration", e)
	}
	before, e := pg.PaymentOrder(u.ID, o.ID)
	if e != nil || before.PaymentState != "paid" || before.FulfillmentState != "pending" || before.TemplateSnapshot.ID != o.TemplateID {
		t.Fatal("payment state/snapshot lost", before, e)
	}
	if e = pg.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); e != nil {
		t.Fatal("migrated signature key failed", e)
	}
	if _, e = pg.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	if _, e = pg.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	fulfilled, e := pg.PaymentOrder(u.ID, o.ID)
	if e != nil || fulfilled.SubscriptionID == "" || fulfilled.Status != "fulfilled" {
		t.Fatal(fulfilled, e)
	}
	var count int
	if e = pg.DB.QueryRow("SELECT COUNT(*) FROM portal_subscriptions WHERE grant_key=?", "payment:"+o.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("migration replay duplicated subscription", count, e)
	}
	wrong := t.TempDir()
	if _, e = secure.Open(filepath.Join(wrong, "master.key")); e != nil {
		t.Fatal(e)
	}
	if opened, e := OpenPostgres(wrong, s.BaseURL, dsn); e == nil {
		opened.Close()
		t.Fatal("wrong restored master key accepted")
	}
}

func TestPaymentSQLiteSnapshotRestoresPendingOrderAndRejectsWrongKey(t *testing.T) {
	s, now, u, _, o := sqlitePaymentSnapshotFixture(t)
	snapshot := paymentSnapshot(t, s)
	restored, e := Open(snapshot, s.BaseURL)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	restored.Now = func() time.Time { return *now }
	if e = restored.CheckIntegrity(); e != nil {
		t.Fatal(e)
	}
	order, e := restored.PaymentOrder(u.ID, o.ID)
	if e != nil || order.PayURL != o.PayURL || order.OrderNo != o.OrderNo {
		t.Fatal("backup lost pending payment", order, e)
	}
	if e = restored.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); e != nil {
		t.Fatal(e)
	}
	if _, e = restored.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	if _, e = restored.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = restored.DB.QueryRow("SELECT COUNT(*) FROM portal_subscriptions WHERE grant_key=?", "payment:"+o.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("restore duplicated grant", count, e)
	}
	wrongVault, e := secure.Open(filepath.Join(t.TempDir(), "master.key"))
	if e != nil {
		t.Fatal(e)
	}
	if e = validateVault(context.Background(), restored.DB, wrongVault); e == nil {
		t.Fatal("backup validated with wrong key")
	}
	// A valid base key does not excuse a corrupt or separately encrypted payment
	// record. Both historical configuration and redirect URLs are integrity data.
	for _, query := range []string{"UPDATE payment_config_versions SET secret=? WHERE version=1", "UPDATE payment_orders SET pay_url=? WHERE id='" + o.ID + "'"} {
		foreign, e := wrongVault.Encrypt("foreign ciphertext")
		if e != nil {
			t.Fatal(e)
		}
		tx, e := restored.DB.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(query, foreign); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = validateVault(context.Background(), tx, restored.Vault); e == nil {
			tx.Rollback()
			t.Fatal("corrupt payment secret escaped integrity check")
		}
		tx.Rollback()
	}
}

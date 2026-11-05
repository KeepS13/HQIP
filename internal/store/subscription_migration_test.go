package store

import (
	"errors"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func legacySubscriptionFixture(t *testing.T, s *Store, node string) {
	t.Helper()
	_, err := s.DB.Exec(`
DROP TABLE portal_requests; DROP TABLE portal_rates; DROP TABLE portal_leases;
DROP TABLE portal_credentials; DROP TABLE portal_usage; DROP TABLE portal_spent_refresh;
DROP TABLE portal_sessions; DROP TABLE portal_authorizations; DROP TABLE portal_subscriptions;
DROP TABLE portal_users; DROP TABLE portal_templates;
CREATE TABLE portal_users(id TEXT PRIMARY KEY,email TEXT,subscription_hash TEXT);
CREATE UNIQUE INDEX portal_subscriptions ON portal_users(subscription_hash);
CREATE TABLE portal_plans(id TEXT PRIMARY KEY);
CREATE TABLE portal_usage(user_id TEXT);
CREATE TABLE portal_credentials(user_id TEXT);
CREATE TABLE portal_sessions(id TEXT PRIMARY KEY);
CREATE TABLE portal_spent_refresh(id TEXT);
CREATE TABLE portal_authorizations(id TEXT);
CREATE TABLE portal_leases(id TEXT PRIMARY KEY,node_id TEXT,user_id TEXT,upload INTEGER,download INTEGER,budget INTEGER,closed INTEGER);
CREATE TABLE portal_rates(lease_id TEXT PRIMARY KEY,version INTEGER);
INSERT INTO portal_users VALUES('old-user','dev@example.invalid','old-token');
INSERT INTO portal_plans VALUES('old-plan');
UPDATE metadata SET value='4' WHERE key='schema_version';
INSERT OR REPLACE INTO metadata VALUES('portal_enabled','true'),('portal_registration','false'),('portal_recovery_hold','true');
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO portal_leases VALUES('old-lease',?,'old-user',100,200,1024,1)", node); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO portal_rates VALUES('old-lease',3)"); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionMigrationRequiresExplicitDrainAndPreservesHistory(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "migration-node")
	legacySubscriptionFixture(t, s, n.ID)
	if _, err := s.DB.Exec("INSERT INTO daily VALUES(?,'user_old-user',?,100,200)", n.ID, DayStart(*now)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO lifetime VALUES(?,'user_old-user',100,200)", n.ID); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(s.DataDir, s.BaseURL); err == nil {
		opened.Close()
		t.Fatal("Open erased development users without deployment gate")
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM portal_users").Scan(&count); err != nil || count != 1 {
		t.Fatal("failed migration changed accounts")
	}
	if _, err := s.DB.Exec("INSERT INTO metadata VALUES('subscriptions_reset_ready','true'),('subscriptions_registration_after_reset','true'),('subscriptions_migration_id','old-migration');UPDATE portal_leases SET closed=0"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.DB); err == nil {
		t.Fatal("gate alone erased unsettled lease")
	}
	if _, err := s.DB.Exec("UPDATE portal_leases SET closed=1"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.DB); err != nil {
		t.Fatal(err)
	}
	if !s.PortalEnabled() || s.PortalSetting("portal_registration") != "true" || s.PortalSetting("portal_recovery_hold") != "false" {
		t.Fatal("operational settings not restored")
	}
	if s.PortalSetting("subscriptions_migration_id") != "" {
		t.Fatal("completed migration retained operational generation marker")
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM portal_users").Scan(&count); err != nil || count != 0 {
		t.Fatal("old users survived development reset")
	}
	if s.PortalSetting("schema_version") != "5" {
		t.Fatal("schema version not advanced")
	}
	if _, err := s.DB.Exec("SELECT * FROM portal_plans"); err == nil {
		t.Fatal("legacy plan table retained")
	}
	desired, _ := s.Desired(n.ID)
	r := model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Daily: []model.UsageBucket{{DeviceID: "user_old-user", Start: DayStart(*now), Upload: 150, Download: 250}}}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	node, err := s.Node(n.ID)
	if err != nil || node.TotalUpload != 150 || node.TotalDownload != 250 {
		t.Fatalf("historical late report lost or duplicated %+v %v", node, err)
	}
	receipt, err := s.RenewLease(n.ID, "old-lease", model.LeaseUsage{UploadBytes: 100, DownloadBytes: 200, Closed: true, AppliedRateVersion: 3})
	if err != nil || !receipt.Closed || receipt.AccountingID != "user_old-user" {
		t.Fatal("closed receipt did not survive account deletion", err)
	}
	if _, err = s.RenewLease(n.ID, "old-lease", model.LeaseUsage{UploadBytes: 100, DownloadBytes: 200, Closed: false}); !errors.Is(err, ErrConflict) {
		t.Fatal("archived lease reopened", err)
	}
	if _, err = s.RenewLease("foreign-node", "old-lease", model.LeaseUsage{Closed: true}); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-node historical settlement accepted", err)
	}
	if err = migrate(s.DB); err != nil {
		t.Fatal("v5 reopening failed", err)
	}
}

func TestSubscriptionMigrationEmptyDevelopmentDatabaseCanUpgrade(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "empty-migration")
	legacySubscriptionFixture(t, s, n.ID)
	if _, err := s.DB.Exec("DELETE FROM portal_rates;DELETE FROM portal_leases;DELETE FROM portal_users;DELETE FROM portal_plans"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterPortal("new@example.invalid", "六个中文字符"); err != nil {
		t.Fatal(err)
	}
}

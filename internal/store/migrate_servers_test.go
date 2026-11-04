package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestVersionTwoServerMigrationPreservesIDsGrantsAndCounters(t *testing.T) {
	s, now := fixture(t)
	a := newNode(t, s, "legacy-good")
	b := newNode(t, s, "legacy-owner-conflict")
	d := newDevice(t, s, "existing-device")
	report(t, s, a, d, *now, 123, 456)
	pass, _ := s.Credential(a.ID, d.ID)
	assignment, _ := s.Assignment(d.ID)
	oldToken := d.SubscriptionURL
	// Recreate the schema-two shape on disk, retaining real encrypted secrets,
	// account grants, timestamps and counters from a working store.
	legacyA := model.Node{ID: a.ID, Name: a.Name, SSHHost: a.SSHHost, SSHPort: 22, SSHUser: "root", PublicIP: a.PublicIP, Domain: a.Domain, Port: 443, Status: "ready", HostKey: "SHA256:cloned-image-key", CreatedAt: a.CreatedAt}
	legacyB := model.Node{ID: b.ID, Name: b.Name, SSHHost: b.SSHHost, SSHPort: 22, SSHUser: "root", PublicIP: b.PublicIP, Domain: b.Domain, Port: 443, Status: "error", Message: "owner-conflict", HostKey: "SHA256:cloned-image-key", CreatedAt: b.CreatedAt}
	for _, n := range []model.Node{legacyA, legacyB} {
		if _, err := s.DB.Exec("UPDATE nodes SET config=? WHERE id=?", jsonString(n), n.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		"DROP TABLE node_preflights", "DROP TABLE node_retirements", "DROP TABLE server_addresses", "DROP TABLE servers",
		"ALTER TABLE nodes DROP COLUMN deleted_at", "ALTER TABLE nodes DROP COLUMN lifecycle", "ALTER TABLE nodes DROP COLUMN revision", "ALTER TABLE nodes DROP COLUMN managed", "ALTER TABLE nodes DROP COLUMN capabilities", "ALTER TABLE nodes DROP COLUMN certificate_status",
		"ALTER TABLE devices DROP COLUMN deleted_at", "ALTER TABLE jobs DROP COLUMN options",
		"UPDATE metadata SET value='2' WHERE key='schema_version'", "DELETE FROM metadata WHERE key='controller_id'",
	} {
		if _, err := s.DB.Exec(stmt); err != nil {
			t.Fatalf("fixture %s: %v", stmt, err)
		}
	}
	dir := s.DataDir
	s.Close()
	upgraded, err := Open(dir, s.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	upgraded.Now = func() time.Time { return *now }
	afterA, err := upgraded.Node(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterB, err := upgraded.Node(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterA.ServerID == "" || afterA.AddressID == "" || !afterA.Managed || afterA.InstallationVersion != 1 || afterA.AuthPort != 18081 || afterA.StatsPort != 18082 {
		t.Fatalf("bad legacy mapping: %+v", afterA)
	}
	if afterB.Managed {
		t.Fatal("owner conflict marked owned")
	}
	if afterA.ServerID == afterB.ServerID {
		t.Fatal("cloned host keys merged without machine identity")
	}
	got, _ := upgraded.Device(d.ID)
	if got.SubscriptionURL != oldToken || got.TotalUpload != 123 || got.TotalDownload != 456 {
		t.Fatal("migration lost subscription or history")
	}
	gotPass, _ := upgraded.Credential(a.ID, d.ID)
	if gotPass != pass {
		t.Fatal("migration rotated existing credential")
	}
	gotAssignment, _ := upgraded.Assignment(d.ID)
	if gotAssignment.Revision != assignment.Revision || gotAssignment.DefaultNodeID != assignment.DefaultNodeID || len(gotAssignment.NodeIDs) != 2 {
		t.Fatal("migration changed assignments")
	}
	controller, err := upgraded.ControllerID()
	if err != nil || controller == "" {
		t.Fatal("controller identity not generated")
	}
	if err = upgraded.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	again, err := Open(dir, s.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	controllerAgain, _ := again.ControllerID()
	if controllerAgain != controller {
		t.Fatal("controller identity changed on restart")
	}
	var version string
	if err = again.DB.QueryRow("SELECT value FROM metadata WHERE key='schema_version'").Scan(&version); err != nil || version != "4" {
		t.Fatal("migration not committed")
	}
}

func TestSchemaThreeMigrationRollsBackOnMalformedNode(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "hqip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO nodes(id,config,agent_hash,agent_secret) VALUES('bad','{','hash','secret')"); err != nil {
		t.Fatal(err)
	}
	if err = migrate(db); err == nil {
		t.Fatal("malformed config migration accepted")
	}
	var version string
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='schema_version'").Scan(&version); err != nil || version != "1" {
		t.Fatal("failed migration changed version")
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='servers'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed migration left partial tables")
	}
}

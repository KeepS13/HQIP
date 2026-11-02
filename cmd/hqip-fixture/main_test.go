package main

import (
	"errors"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func fixtureStore(t *testing.T) (*store.Store, model.Node) {
	t.Helper()
	db, err := store.Open(t.TempDir(), "https://test.example.com")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a, err := db.CreateNode(model.Node{Name: "existing A", SSHHost: "203.0.113.10", PublicIP: "203.0.113.10", Domain: "test.example.com", Port: 443, SSHPort: 22, SSHUser: "root", HostKey: "PINNED"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetStatus(a.ID, "ready", "existing ready node", true); err != nil {
		t.Fatal(err)
	}
	a, err = db.Node(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return db, a
}

func TestFixtureHasIndependentCredentialsWithoutInstallerAndPreservesHistory(t *testing.T) {
	db, a := fixtureStore(t)
	var originalConfig string
	if err := db.DB.QueryRow("SELECT config FROM nodes WHERE id=?", a.ID).Scan(&originalConfig); err != nil {
		t.Fatal(err)
	}
	f, err := prepareFixture(db, a.ID, func(f fixture) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.Node(f.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Port != 8443 || b.Status != "awaiting_external" || b.HostKey != a.HostKey || b.ControlOnNode || b.ACMEViaCaddy {
		t.Fatal("incorrect isolated fixture topology")
	}
	var jobs int
	if err = db.DB.QueryRow("SELECT COUNT(*) FROM jobs WHERE node_id=?", b.ID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("fixture enqueued a production installer")
	}
	var after string
	if err = db.DB.QueryRow("SELECT config FROM nodes WHERE id=?", a.ID).Scan(&after); err != nil || after != originalConfig {
		t.Fatal("fixture changed node A configuration")
	}
	assignment, err := db.Assignment(f.DeviceID)
	if err != nil || assignment.DefaultNodeID != a.ID || len(assignment.NodeIDs) != 2 {
		t.Fatal("fixture device not assigned A and B")
	}
	var encryptedA, encryptedB string
	_ = db.DB.QueryRow("SELECT password FROM credentials WHERE node_id=? AND device_id=?", a.ID, f.DeviceID).Scan(&encryptedA)
	_ = db.DB.QueryRow("SELECT password FROM credentials WHERE node_id=? AND device_id=?", b.ID, f.DeviceID).Scan(&encryptedB)
	passwordA, _ := db.Vault.Decrypt(encryptedA)
	passwordB, _ := db.Vault.Decrypt(encryptedB)
	if passwordA == "" || passwordB == "" || passwordA == passwordB || passwordB == f.ProbePassword {
		t.Fatal("fixture credentials were reused")
	}
	if _, err = prepareFixture(db, a.ID, func(f fixture) error { return nil }); err == nil {
		t.Fatal("duplicate fixture was accepted")
	}
	if err = cleanupFixture(db, f); err != nil {
		t.Fatal(err)
	}
	d, err := db.Device(f.DeviceID)
	if err != nil || d.Enabled {
		t.Fatal("fixture device not preserved disabled")
	}
	b, err = db.Node(b.ID)
	if err != nil || b.Status != "fixture_disabled" {
		t.Fatal("fixture node not preserved disabled")
	}
	afterA, err := db.Node(a.ID)
	if err != nil || afterA.Status != "ready" {
		t.Fatal("cleanup disabled existing A")
	}
	assignment, err = db.Assignment(f.DeviceID)
	if err != nil || len(assignment.NodeIDs) != 0 {
		t.Fatal("fixture grants remain")
	}
	if err = cleanupFixture(db, f); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
}

func TestFixtureOutputFailureRollsBackRegistration(t *testing.T) {
	db, a := fixtureStore(t)
	if _, err := prepareFixture(db, a.ID, func(f fixture) error { return errors.New("disk write failed") }); err == nil {
		t.Fatal("write error ignored")
	}
	var count int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count); err != nil || count != 1 {
		t.Fatal("partial node left after failed export")
	}
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM devices").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial device left after failed export")
	}
}

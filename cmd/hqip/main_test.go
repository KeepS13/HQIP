package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
	"github.com/KeepS13/HQIP/internal/store"
)

func TestAdministratorOriginRequiresHTTPSExceptLocalDevelopment(t *testing.T) {
	for _, address := range []string{"https://hqip.5202345.xyz", "https://panel.example.com:8443/", "http://127.0.0.1:8080"} {
		if err := validateAdminOrigin(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"http://panel.example.com", "https:///", "https://user:pass@example.com", "https://example.com/path", "https://example.com?x=1", "https://example.com?", "https://example.com/#fragment", "//example.com", "ftp://example.com"} {
		if validateAdminOrigin(address) == nil {
			t.Errorf("accepted %s", address)
		}
	}
}

func TestInitializeEmptySiteWithRulesAndRejectReinitialization(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	bundle := rules.Default()
	bundle.Source.Name = "deployment-template"
	body, _ := json.Marshal(bundle)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"init-admin", "--data-dir", filepath.Join(dir, "data"), "--output", filepath.Join(dir, "admin.json"), "--rules", path}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	if err := run(args); err == nil {
		t.Fatal("reinitialized existing site")
	}
	s, err := store.Open(filepath.Join(dir, "data"), "https://hqip.5202345.xyz")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.Rules()
	if err != nil || v.Bundle.Source.Name != bundle.Source.Name {
		t.Fatalf("rules: %+v %v", v, err)
	}
	for _, table := range []string{"nodes", "devices", "client_sessions"} {
		var count int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count %d: %v", table, count, err)
		}
	}
}

func TestBackupRestoreRoundTripIncludesMatchingMasterKey(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	credentials := filepath.Join(root, "initial.json")
	if err := run([]string{"init-admin", "--data-dir", data, "--output", credentials}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	var initial map[string]string
	if err = json.Unmarshal(raw, &initial); err != nil || initial["username"] != "admin" || len(initial["password"]) < 32 {
		t.Fatal("initial credentials file invalid")
	}
	db, err := store.Open(data, "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateDevice("portable-device")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	archive := filepath.Join(root, "backup.tar.gz")
	if err = run([]string{"backup", "--data-dir", data, "--output", archive}); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err = run([]string{"restore", "--data-dir", restored, "--input", archive}); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(restored, "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Device(d.ID)
	if err != nil || got.SubscriptionURL != d.SubscriptionURL {
		t.Fatal("restored database cannot decrypt original device credentials")
	}
	if err = run([]string{"restore", "--data-dir", restored, "--input", archive}); err == nil {
		t.Fatal("restore overwrote an existing database")
	}
}

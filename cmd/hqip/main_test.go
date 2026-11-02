package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/store"
)

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

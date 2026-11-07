package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"

	"github.com/KeepS13/HQIP/internal/store"
)

// storage-info is a local deployment probe, never an HTTP endpoint. It emits
// schema capabilities and a key fingerprint without database URLs or secrets.
func storageInfo(args []string) error {
	f := flag.NewFlagSet("storage-info", flag.ContinueOnError)
	dir := f.String("data-dir", "/var/lib/hqip", "配套数据目录")
	if err := f.Parse(args); err != nil {
		return err
	}
	s, err := store.OpenConfigured(*dir, "", os.Getenv("HQIP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer s.Close()
	var schema string
	if err = s.DB.QueryRow("SELECT value FROM metadata WHERE key='schema_version'").Scan(&schema); err != nil {
		return err
	}
	version, err := strconv.Atoi(schema)
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(*dir, "master.key"))
	if err != nil {
		return err
	}
	h := sha256.Sum256(key)
	backend, pgVersion := "sqlite", 0
	if s.Postgres() {
		backend, pgVersion = "postgresql", 1
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"backend": backend, "schemaVersion": version, "postgresSchemaVersion": pgVersion, "serverBillingVersion": 2, "masterSHA256": hex.EncodeToString(h[:])})
}

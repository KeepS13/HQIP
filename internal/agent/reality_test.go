package agent

import (
	"crypto/ecdh"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestRealityIdentityPersistsAndMalformedFileDoesNotRotate(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.db")
	first, err := loadRealityIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadRealityIdentity(state)
	if err != nil || first != second {
		t.Fatal("restart rotated Reality identity", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ecdh.X25519().NewPrivateKey(raw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(state), "reality-identity.json")
	if err = os.WriteFile(path, []byte("corrupt identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadRealityIdentity(state); err == nil {
		t.Fatal("corrupt existing identity silently replaced")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "corrupt identity" {
		t.Fatal("corrupt identity was overwritten")
	}
}

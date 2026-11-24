//go:build windows

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

// Exercise actual child selection after an installer replaces bundled files.
// Only disposable test executables run; no installed client or account is used.
func TestInstallerUpgradeSkipsOlderPointers(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	oldKey, oldSequence := updateKey, bundledSequence
	updateKey, bundledSequence = hex.EncodeToString(public), "5000019"
	t.Cleanup(func() { updateKey, bundledSequence = oldKey, oldSequence })
	t.Setenv("HQIP_LAUNCHER_TEST_HELPER", "1")
	for _, sequence := range []int64{5000017, 5000020} {
		root := t.TempDir()
		version := strings.Repeat("b", 64)
		helperVersion(t, root, "bundled", "success")
		helperVersion(t, root, version, "success")
		envelope, err := clientrelease.Sign(clientrelease.Release{Schema: 1, Version: "5.0.0", Sequence: sequence, Platform: "windows-amd64", SHA256: version, Size: 1, Filename: clientrelease.Filename("windows-amd64"), APIMin: 3, APIMax: 3}, private)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(envelope)
		for _, pointer := range []string{"active.json", "pending.json"} {
			if err = os.WriteFile(filepath.Join(root, pointer), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err = launch(root); err != nil {
			t.Fatal(err)
		}
		_, bundledErr := os.Stat(filepath.Join(root, "versions/bundled/started"))
		_, updatedErr := os.Stat(filepath.Join(root, "versions", version, "started"))
		if sequence < 5000019 && (bundledErr != nil || !os.IsNotExist(updatedErr)) {
			t.Fatal("older update pointer masked the new installer payload")
		}
		if sequence > 5000019 && (updatedErr != nil || !os.IsNotExist(bundledErr)) {
			t.Fatal("installer suppressed a newer authenticated update")
		}
	}
}

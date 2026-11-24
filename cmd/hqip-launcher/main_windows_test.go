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
	"time"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

// The child is this test binary, with a tiny protocol-compatible fake UI. It
// never opens a window, touches proxy settings, or uses a real account directory.
func TestMain(m *testing.M) {
	if os.Getenv("HQIP_LAUNCHER_TEST_HELPER") == "1" {
		mode, _ := os.ReadFile("helper-mode")
		_ = os.WriteFile("started", []byte("yes"), 0600)
		if string(mode) == "hang" {
			time.Sleep(10 * time.Second)
			os.Exit(2)
		}
		if string(mode) == "early-exit" {
			os.Exit(1)
		}
		for _, arg := range os.Args {
			if strings.HasPrefix(arg, "--startup-marker=") {
				_ = os.WriteFile(strings.TrimPrefix(arg, "--startup-marker="), []byte("ready"), 0600)
			}
		}
		time.Sleep(350 * time.Millisecond)
		if string(mode) == "crash-after-ready" {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helperVersion(t *testing.T, root, version, mode string) {
	t.Helper()
	dir := filepath.Join(root, "versions", version)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "HQIP-UI.exe"), raw, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "helper-mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherActivatesOrRollsBackCompleteVersion(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	updateKey = hex.EncodeToString(public)
	t.Setenv("HQIP_LAUNCHER_TEST_HELPER", "1")
	for _, mode := range []string{"success", "early-exit", "crash-after-ready", "hang"} {
		t.Run(mode, func(t *testing.T) {
			startupTimeout = 2 * time.Second
			if mode == "hang" {
				startupTimeout = 150 * time.Millisecond
			}
			root := t.TempDir()
			version := strings.Repeat("a", 64)
			helperVersion(t, root, "bundled", "success")
			helperVersion(t, root, version, mode)
			envelope, err := clientrelease.Sign(clientrelease.Release{Schema: 1, Version: "5.0.1", Sequence: 5000002, Platform: "windows-amd64", SHA256: version, Size: 1, Filename: clientrelease.Filename("windows-amd64"), APIMin: 3, APIMax: 3}, private)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(envelope)
			if err = os.WriteFile(filepath.Join(root, "pending.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = launch(root); err != nil {
				t.Fatal(err)
			}
			_, activeErr := os.Stat(filepath.Join(root, "active.json"))
			_, fallbackErr := os.Stat(filepath.Join(root, "versions/bundled/started"))
			if mode == "success" && (activeErr != nil || !os.IsNotExist(fallbackErr)) {
				t.Fatal("healthy candidate did not activate exclusively")
			}
			if mode != "success" && (!os.IsNotExist(activeErr) || fallbackErr != nil) {
				t.Fatal("failed candidate did not roll back")
			}
			if _, err = os.Stat(filepath.Join(root, "pending.json")); !os.IsNotExist(err) {
				t.Fatal("pending pointer survived")
			}
		})
	}
	startupTimeout = 30 * time.Second
}

//go:build windows

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/flutterrelease"
)

func TestMain(m *testing.M) {
	if os.Getenv("HQIP_PREVIEW_LAUNCHER_FIXTURE") == "1" {
		mode, _ := os.ReadFile("fixture-mode")
		if string(mode) == "fail" {
			os.Exit(1)
		}
		for i, arg := range os.Args {
			if arg == "--startup-marker" && i+1 < len(os.Args) {
				_ = os.WriteFile(os.Args[i+1], []byte("ready"), 0600)
			}
		}
		time.Sleep(400 * time.Millisecond)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func checksum(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func prepareFixture(t *testing.T, root, version, mode string) flutterrelease.Release {
	t.Helper()
	dir := filepath.Join(root, "versions", version)
	_ = os.MkdirAll(filepath.Join(dir, "data"), 0700)
	exe, _ := os.Executable()
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"hqip-runtime.exe": binary, "hqip-ui.exe": []byte("ui"), "flutter_windows.dll": []byte("flutter"), "data/icudtl.dat": []byte("icu"), "sing-box.exe": []byte("core"), "hqip-helper.exe": []byte("helper"), "libcronet.dll": []byte("cronet"), "fixture-mode": []byte(mode)}
	manifest := flutterrelease.Manifest{}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), raw, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, flutterrelease.File{Path: name, Size: int64(len(raw)), SHA256: checksum(raw)})
	}
	raw, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(dir, "payload.json"), raw, 0600)
	return flutterrelease.Release{Schema: 2, Product: flutterrelease.Product, Channel: flutterrelease.Channel, Version: "preview-2", Sequence: 2, Platform: "windows-amd64", Kind: "update", Filename: flutterrelease.Filename("windows-amd64", "update"), SHA256: version, Size: 1, ManifestSHA256: checksum(raw)}
}
func TestPreviewLauncherActivatesAndRollsBack(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	updateKey = hex.EncodeToString(public)
	t.Setenv("HQIP_PREVIEW_LAUNCHER_FIXTURE", "1")
	for _, mode := range []string{"success", "fail", "tamper"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			prepareFixture(t, root, "bundled", "success")
			version := strings.Repeat("a", 64)
			r := prepareFixture(t, root, version, mode)
			e, err := flutterrelease.Sign(r, private)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(e)
			_ = os.WriteFile(filepath.Join(root, "pending.json"), raw, 0600)
			if mode == "tamper" {
				_ = os.WriteFile(filepath.Join(root, "versions", version, "flutter_windows.dll"), []byte("modified"), 0600)
			}
			if restart, err := launch(root); err != nil || restart {
				t.Fatal(restart, err)
			}
			active, err := os.ReadFile(filepath.Join(root, "active.json"))
			if mode == "success" && (err != nil || string(active) != string(raw)) {
				t.Fatal("healthy version not activated")
			}
			if mode != "success" && !os.IsNotExist(err) {
				t.Fatal("failed version activated")
			}
			if _, err := os.Stat(filepath.Join(root, "pending.json")); !os.IsNotExist(err) {
				t.Fatal("pending survived")
			}
		})
	}
}

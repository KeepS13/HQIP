//go:build windows

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/KeepS13/HQIP/internal/flutterrelease"
)

func installerMetadata(t *testing.T, root, version string, sequence int64) {
	t.Helper()
	raw, _ := json.Marshal(previewInstallMetadata{flutterrelease.Product, flutterrelease.Channel, version, sequence})
	if err := os.WriteFile(filepath.Join(root, "install.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func installerPointer(t *testing.T, root, name string, sequence int64, key ed25519.PrivateKey) []byte {
	t.Helper()
	release := flutterrelease.Release{Schema: 2, Product: flutterrelease.Product, Channel: flutterrelease.Channel,
		Version: "0.1.0-preview.5", Sequence: sequence, Platform: "windows-amd64", Kind: "update",
		Filename: flutterrelease.Filename("windows-amd64", "update"), SHA256: strings.Repeat("a", 64), Size: 10, ManifestSHA256: strings.Repeat("b", 64)}
	envelope, err := flutterrelease.Sign(release, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(envelope)
	if err = os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInstallerDiscoversCustomLegacyDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "软件 HQIP Preview")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, legacyVersion := range []string{"0.1.0-preview.1", "0.1.0-preview.2", "0.1.0-preview.3"} {
		// Duplicate views commonly describe the same per-user installation.
		records := []previewInstallRecord{{root + `\`, legacyVersion}, {root, legacyVersion}}
		result, err := inspectPreviewInstallation(t.TempDir(), records, 4, "")
		if err != nil || result.Directory != root || result.Version != legacyVersion || result.Mode != "upgrade" {
			t.Fatalf("legacy discovery: %+v, %v", result, err)
		}
	}
}

func TestInstallerPlansNewRepairUpgradeAndDowngrade(t *testing.T) {
	root := t.TempDir()
	result, err := inspectPreviewInstallation(root, nil, 4, "")
	if err != nil || result.Mode != "install" {
		t.Fatalf("first installation: %+v, %v", result, err)
	}
	installerMetadata(t, root, "0.1.0-preview.4", 4)
	for _, test := range []struct {
		incoming int64
		mode     string
	}{{3, "downgrade"}, {4, "repair"}, {5, "upgrade"}} {
		result, err := inspectPreviewInstallation(root, nil, test.incoming, "")
		if err != nil || result.Mode != test.mode || result.Sequence != 4 {
			t.Fatalf("incoming %d: %+v, %v", test.incoming, result, err)
		}
	}
}

func TestInstallerProtectsAuthenticatedActiveAndPendingVersions(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	for _, name := range []string{"active.json", "pending.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			installerMetadata(t, root, "0.1.0-preview.2", 2)
			before := installerPointer(t, root, name, 5, private)
			for _, test := range []struct {
				incoming int64
				mode     string
			}{{4, "downgrade"}, {5, "repair"}, {6, "upgrade"}} {
				result, err := inspectPreviewInstallation(root, nil, test.incoming, hex.EncodeToString(public))
				if err != nil || result.Mode != test.mode || result.Sequence != 5 || result.Version != "0.1.0-preview.5" {
					t.Fatalf("higher %s: %+v, %v", name, result, err)
				}
			}
			after, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || string(after) != string(before) {
				t.Fatal("inspection modified an installed update pointer")
			}
		})
	}
}

func TestInstallerKeepsHighestRecordedVersionAfterInterruptedInstall(t *testing.T) {
	root := t.TempDir()
	installerMetadata(t, root, "0.1.0-preview.4", 4)
	for _, registered := range []string{"0.1.0-preview.2", "0.1.0-preview.5"} {
		result, err := inspectPreviewInstallation(root, []previewInstallRecord{{root, registered}}, 3, "")
		if err != nil || result.Mode != "downgrade" || result.Sequence < 4 {
			t.Fatalf("partial installation could downgrade: %+v, %v", result, err)
		}
		if strings.HasSuffix(registered, ".5") && result.Sequence != 5 {
			t.Fatal("newer registry version lost")
		}
	}
}

func TestInstallerRejectsUnknownOrCorruptExistingState(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	for _, mode := range []string{"unknown-binary", "unknown-registry", "bad-metadata", "wrong-product", "bad-pointer", "forged-pointer", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			var records []previewInstallRecord
			switch mode {
			case "unknown-binary":
				_ = os.WriteFile(filepath.Join(root, "HQIP Preview.exe"), []byte("old"), 0600)
			case "unknown-registry":
				records = []previewInstallRecord{{root, "unknown"}}
			case "bad-metadata":
				_ = os.WriteFile(filepath.Join(root, "install.json"), []byte("{"), 0600)
			case "wrong-product":
				_ = os.WriteFile(filepath.Join(root, "install.json"), []byte(`{"product":"hqip","channel":"stable","version":"1.0.0","sequence":5000022}`), 0600)
			case "bad-pointer":
				_ = os.WriteFile(filepath.Join(root, "active.json"), []byte("{"), 0600)
			case "forged-pointer":
				_, other, _ := ed25519.GenerateKey(rand.Reader)
				installerPointer(t, root, "active.json", 5, other)
			case "oversize":
				_ = os.WriteFile(filepath.Join(root, "pending.json"), []byte(strings.Repeat("x", 65537)), 0600)
			}
			if _, err := inspectPreviewInstallation(root, records, 4, hex.EncodeToString(public)); err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
	root := t.TempDir()
	installerMetadata(t, root, "0.1.0-preview.1", 1)
	installerPointer(t, root, "active.json", 5, private)
	if _, err := inspectPreviewInstallation(root, nil, 6, "invalid-key"); err == nil {
		t.Fatal("accepted an unverifiable update pointer")
	}
}

func TestInstallerRejectsConflictingRegistrations(t *testing.T) {
	root := t.TempDir()
	for _, records := range [][]previewInstallRecord{
		{{root, "0.1.0-preview.1"}, {t.TempDir(), "0.1.0-preview.1"}},
		{{root, "0.1.0-preview.1"}, {root, "0.1.0-preview.2"}},
		{{`relative\Preview`, "0.1.0-preview.1"}},
	} {
		if _, err := inspectPreviewInstallation(root, records, 4, ""); err == nil {
			t.Fatalf("accepted conflicting records: %+v", records)
		}
	}
}

func TestInstallerReportPreservesUnicodePathsAndPreventsINIInjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.ini")
	result := installedPreview{Directory: `D:\软件\HQIP Preview`, Version: "0.1.0-preview.3", Sequence: 3, Mode: "blocked", Message: "error\r\nMode=repair"}
	if err := writeInstallerReport(path, result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 2 || raw[0] != 0xff || raw[1] != 0xfe {
		t.Fatalf("not a Unicode Windows INI: %v", err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	text := string(utf16.Decode(units))
	if !strings.Contains(text, "Directory="+result.Directory+"\r\n") || strings.Contains(text, "\r\nMode=repair") {
		t.Fatalf("unsafe or corrupted INI: %q", text)
	}
}

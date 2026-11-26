package flutterrelease

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func fixture(t *testing.T, extra string) (string, Release) {
	t.Helper()
	names := []string{"hqip-ui.exe", "hqip-runtime.exe", "flutter_windows.dll", "data/icudtl.dat", "sing-box.exe", "hqip-helper.exe", "libcronet.dll"}
	m := Manifest{}
	for _, name := range names {
		m.Files = append(m.Files, File{name, int64(len(name)), hash([]byte(name))})
	}
	raw, _ := json.Marshal(m)
	p := filepath.Join(t.TempDir(), "package.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for _, name := range names {
		w, _ := z.Create(name)
		_, _ = w.Write([]byte(name))
	}
	w, _ := z.Create("payload.json")
	_, _ = w.Write(raw)
	if extra != "" {
		w, _ = z.Create(extra)
		_, _ = w.Write([]byte("unlisted"))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	data, _ := os.ReadFile(p)
	r := Release{Schema: 2, Product: Product, Channel: Channel, Version: "0.1.0-preview.2", Sequence: 2, Platform: "windows-amd64", Kind: "update", Filename: Filename("windows-amd64", "update"), SHA256: hash(data), Size: int64(len(data)), ManifestSHA256: hash(raw)}
	return p, r
}

func TestSignedPreviewCannotCrossChannelsOrBeTampered(t *testing.T) {
	_, r := fixture(t, "")
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	e, err := Sign(r, private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Verify(hex.EncodeToString(public)); err != nil {
		t.Fatal(err)
	}
	e.Payload = strings.ReplaceAll(e.Payload, "a", "b")
	if _, err = e.Verify(hex.EncodeToString(public)); err == nil {
		t.Fatal("accepted modified metadata")
	}
	r.Channel = "stable"
	if _, err = Sign(r, private); err == nil {
		t.Fatal("accepted production channel")
	}
}

func TestStageDoesNotSwitchActiveAndChecksEveryFile(t *testing.T) {
	p, r := fixture(t, "")
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	e, _ := Sign(r, private)
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "active.json"), []byte("previous"), 0600)
	if err := Stage(root, p, r, e); err != nil {
		t.Fatal(err)
	}
	active, _ := os.ReadFile(filepath.Join(root, "active.json"))
	if string(active) != "previous" {
		t.Fatal("stage activated update")
	}
	if _, err := os.Stat(filepath.Join(root, "pending.json")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "versions", r.SHA256)
	if err := os.WriteFile(filepath.Join(dir, "flutter_windows.dll"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTree(dir, r); err == nil {
		t.Fatal("accepted modified DLL")
	}
	if err := Stage(root, p, r, e); err == nil {
		t.Fatal("reused corrupted staged payload")
	}
}

func TestArchiveRejectsTraversalAndUnlistedCode(t *testing.T) {
	for _, extra := range []string{"../outside.exe", "data/../../outside.exe", "unlisted.dll", "DATA/icudtl.dat", "C:/evil.exe"} {
		t.Run(extra, func(t *testing.T) {
			p, r := fixture(t, extra)
			dest := filepath.Join(t.TempDir(), "payload")
			if err := Extract(p, dest, r); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}

func TestWindowsPathRules(t *testing.T) {
	for _, name := range []string{"../a", "a/../b", "a\\b", "a.", "a ", "NUL.txt", "COM1", "LPT9.exe", "a:b", "/a", ""} {
		if safePath(name) {
			t.Fatalf("accepted %q", name)
		}
	}
	if !safePath("data/flutter_assets/AssetManifest.bin") {
		t.Fatal("rejected normal Flutter asset")
	}
}

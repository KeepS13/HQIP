package clientrelease

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func fixtureRelease(t *testing.T, raw []byte) Release {
	t.Helper()
	digest := sha256.Sum256(raw)
	return Release{Schema: 1, Version: "5.1.0", Sequence: 5010000, Platform: "windows-amd64", Filename: Filename("windows-amd64"), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(raw)), APIMin: 3, APIMax: 3}
}
func TestReleaseSignatureRejectsTamperingAndOtherKeys(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	envelope, err := Sign(fixtureRelease(t, []byte("payload")), private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = envelope.Verify(hex.EncodeToString(public)); err != nil {
		t.Fatal(err)
	}
	if _, err = envelope.Verify(hex.EncodeToString(other)); err == nil {
		t.Fatal("wrong signing key accepted")
	}
	envelope.Payload = envelope.Payload[:len(envelope.Payload)-4] + "AAAA"
	if _, err = envelope.Verify(hex.EncodeToString(public)); err == nil {
		t.Fatal("modified release accepted")
	}
}
func TestWindowsUpdateExtractionRejectsUnsafeAndIncompletePackages(t *testing.T) {
	for _, name := range []string{"../outside.exe", "nested/HQIP-UI.exe", "HQIP-UI.exe:evil", "UNKNOWN.dll", "incomplete", "valid"} {
		t.Run(name, func(t *testing.T) {
			var buffer bytes.Buffer
			archive := zip.NewWriter(&buffer)
			if name != "incomplete" {
				for _, file := range WindowsFiles {
					entry, _ := archive.Create(file)
					entry.Write([]byte("test content"))
				}
			}
			if name != "valid" {
				entry, _ := archive.Create(name)
				entry.Write([]byte("unsafe"))
			}
			archive.Close()
			dir := t.TempDir()
			path := filepath.Join(dir, "package.zip")
			os.WriteFile(path, buffer.Bytes(), 0600)
			destination := filepath.Join(dir, "staged")
			err := ExtractWindows(path, destination, fixtureRelease(t, buffer.Bytes()))
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected extraction result: %v", err)
			}
			if name != "valid" {
				if _, err = os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("invalid archive published a staging directory")
				}
			}
		})
	}
}
func TestVerifyFileRejectsChecksumAndPreservesOpenVersion(t *testing.T) {
	raw := []byte("trusted")
	release := fixtureRelease(t, raw)
	path := filepath.Join(t.TempDir(), "artifact")
	os.WriteFile(path, raw, 0600)
	file, err := VerifyFile(path, release)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	os.WriteFile(path, []byte("mutated"), 0600)
	if file, err = VerifyFile(path, release); err == nil {
		file.Close()
		t.Fatal("same-sized substitution accepted")
	}
}

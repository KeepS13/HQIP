package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

func TestCatalogKeepsPreviousDownloadsAndRejectsDowngrade(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := hex.EncodeToString(pub)
	dir := t.TempDir()
	signed := func(platform string, sequence int64) clientrelease.Envelope {
		r := testRelease([]byte("fixture"))
		r.Platform, r.Filename, r.Sequence = platform, clientrelease.Filename(platform), sequence
		e, err := clientrelease.Sign(r, private)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	for _, e := range []clientrelease.Envelope{signed("android-arm64", 1), signed("windows-amd64", 1), signed("windows-amd64", 2), signed("windows-amd64", 3)} {
		if err := publishCatalog(dir, public, e); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "godot-releases.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog clientrelease.Catalog
	if err := json.Unmarshal(before, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Releases) != 3 {
		t.Fatalf("wanted current + previous Windows and Android, got %d", len(catalog.Releases))
	}
	for i, want := range []int64{3, 2, 1} {
		r, err := catalog.Releases[i].Verify(public)
		if err != nil || r.Sequence != want {
			t.Fatalf("entry %d: %v, %v", i, r, err)
		}
	}
	if err := publishCatalog(dir, public, signed("windows-amd64", 2)); err == nil {
		t.Fatal("accepted downgraded catalog")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed publication changed the visible catalog")
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishCatalog(dir, public, signed("windows-amd64", 4)); err == nil {
		t.Fatal("silently replaced a corrupt catalog")
	}
}

func TestArtifactCopyRejectsChangedSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.zip")
	data := []byte("verified fixture")
	r := testRelease(data)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishArtifact(source, dir, r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("tampered fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishArtifact(source, dir, r); err == nil {
		t.Fatal("accepted source changed after signing")
	}
	file, err := clientrelease.VerifyFile(r.ArtifactPath(dir), r)
	if err != nil {
		t.Fatal("prior artifact was damaged:", err)
	}
	file.Close()
	partials, _ := filepath.Glob(filepath.Join(filepath.Dir(r.ArtifactPath(dir)), ".release-*"))
	if len(partials) != 0 {
		t.Fatal("temporary artifact left behind")
	}
}

func testRelease(data []byte) clientrelease.Release {
	hash := sha256.Sum256(data)
	return clientrelease.Release{Schema: 1, Version: "5.0.1", Sequence: 1, Platform: "windows-amd64", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data)), Filename: clientrelease.Filename("windows-amd64"), APIMin: 3, APIMax: 3}
}

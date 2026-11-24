package control

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

func TestGodotReleaseCatalogAndVerifiedRangeDownload(t *testing.T) {
	dir := t.TempDir()
	server := &Server{opts: Options{ArtifactDir: dir}}
	request := httptest.NewRequest("GET", "/api/v3/client/releases", nil)
	response := httptest.NewRecorder()
	server.godotReleases(response, request)
	if response.Code != 200 || response.Body.String() != "{\"releases\":[]}\n" {
		t.Fatalf("unpublished: %d %s", response.Code, response.Body)
	}
	data := []byte("verified-package")
	hash := sha256.Sum256(data)
	release := clientrelease.Release{Schema: 1, Version: "5.0.1", Sequence: 5000002, Platform: "android-arm64", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data)), Filename: clientrelease.Filename("android-arm64"), APIMin: 3, APIMax: 3}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	envelope, _ := clientrelease.Sign(release, private)
	raw, _ := json.Marshal(clientrelease.Catalog{Releases: []clientrelease.Envelope{envelope}})
	if err := os.WriteFile(filepath.Join(dir, "godot-releases.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(release.ArtifactPath(dir)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release.ArtifactPath(dir), data, 0600); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", release.DownloadPath(), nil)
	request.SetPathValue("sha256", release.SHA256)
	request.SetPathValue("filename", release.Filename)
	request.Header.Set("Range", "bytes=0-7")
	response = httptest.NewRecorder()
	server.godotDownload(response, request)
	if response.Code != 206 || response.Body.String() != "verified" {
		t.Fatalf("range: %d %s", response.Code, response.Body)
	}
	if err := os.WriteFile(release.ArtifactPath(dir), []byte("tampered-package"), 0600); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.godotDownload(response, request)
	if response.Code != 503 {
		t.Fatal("corrupt artifact served")
	}
	request.SetPathValue("sha256", "../../outside")
	response = httptest.NewRecorder()
	server.godotDownload(response, request)
	if response.Code != 404 {
		t.Fatal("unknown artifact path accepted")
	}
}

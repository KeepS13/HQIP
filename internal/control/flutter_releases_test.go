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

	"github.com/KeepS13/HQIP/internal/flutterrelease"
)

func TestFlutterCatalogIsOptInAndDownloadIsVerified(t *testing.T) {
	dir := t.TempDir()
	server := &Server{opts: Options{ArtifactDir: dir}}
	body := []byte("preview-package")
	sum := sha256.Sum256(body)
	r := flutterrelease.Release{Schema: 2, Product: flutterrelease.Product, Channel: flutterrelease.Channel, Version: "preview-1", Sequence: 1, Platform: "android-arm64", Kind: "update", Filename: flutterrelease.Filename("android-arm64", "update"), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	e, _ := flutterrelease.Sign(r, private)
	raw, _ := json.Marshal(flutterrelease.Catalog{Releases: []flutterrelease.Envelope{e}})
	if err := os.WriteFile(filepath.Join(dir, "flutter-preview-releases.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		url   string
		code  int
		empty bool
	}{
		{"/api/v3/client/releases", 200, true},
		{"/api/v3/client/releases?client=flutter&channel=preview", 200, false},
		{"/api/v3/client/releases?client=flutter&channel=stable", 404, false},
	} {
		response := httptest.NewRecorder()
		server.godotReleases(response, httptest.NewRequest("GET", test.url, nil))
		if response.Code != test.code {
			t.Fatal(test.url, response.Code)
		}
		if test.empty && response.Body.String() != "{\"releases\":[]}\n" {
			t.Fatal("preview leaked to old clients")
		}
	}
	_ = os.MkdirAll(filepath.Dir(r.ArtifactPath(dir)), 0700)
	_ = os.WriteFile(r.ArtifactPath(dir), body, 0600)
	request := httptest.NewRequest("GET", r.DownloadPath(), nil)
	request.SetPathValue("sha256", r.SHA256)
	request.SetPathValue("filename", r.Filename)
	request.Header.Set("Range", "bytes=0-6")
	response := httptest.NewRecorder()
	server.flutterDownload(response, request)
	if response.Code != 206 || response.Body.String() != "preview" {
		t.Fatal(response.Code, response.Body)
	}
	_ = os.WriteFile(r.ArtifactPath(dir), []byte("modified-payload"), 0600)
	response = httptest.NewRecorder()
	server.flutterDownload(response, request)
	if response.Code != 503 {
		t.Fatal("corrupt preview served")
	}
}

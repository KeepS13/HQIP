package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortalDownloadValidatesManifestAndFixedPath(t *testing.T) {
	s, _ := newServer(t)
	s.opts.ArtifactDir = t.TempDir()
	h := s.PublicHandler()
	w := request(h, "GET", "/api/v1/user/releases", nil, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"releases":[]`) {
		t.Fatal("missing release not represented safely")
	}
	data := []byte("PK-test-verified-artifact")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if e := os.WriteFile(filepath.Join(s.opts.ArtifactDir, desktopFilename), data, 0600); e != nil {
		t.Fatal(e)
	}
	manifest := desktopRelease{Version: "3.0.0", SourceSHA: strings.Repeat("a", 40), Filename: desktopFilename, SHA256: sha, Size: int64(len(data))}
	raw, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(s.opts.ArtifactDir, "desktop-manifest.json"), raw, 0600)
	w = request(h, "GET", "/api/v1/user/releases", nil, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), s.opts.ArtifactDir) || !strings.Contains(w.Body.String(), sha) {
		t.Fatal("release metadata missing hash or leaked path", w.Body.String())
	}
	w = request(h, "GET", "/downloads/"+desktopFilename, nil, "", "")
	if w.Code != 200 || w.Body.String() != string(data) || w.Header().Get("Content-Length") == "" || w.Header().Get("ETag") != "\""+sha+"\"" {
		t.Fatal("download bytes/hash/length mismatch")
	}
	r := httptest.NewRequest("GET", "/downloads/"+desktopFilename, nil)
	r.Header.Set("Range", "bytes=0-1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "PK" {
		t.Fatal("range download failed")
	}
	if w = request(h, "GET", "/downloads/desktop-manifest.json", nil, "", ""); w.Code != 404 {
		t.Fatal("unlisted artifact accessible")
	}
	_ = os.WriteFile(filepath.Join(s.opts.ArtifactDir, desktopFilename), []byte("tampered"), 0600)
	if w = request(h, "GET", "/downloads/"+desktopFilename, nil, "", ""); w.Code != 404 {
		t.Fatal("tampered release served")
	}
}

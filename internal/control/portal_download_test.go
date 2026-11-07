package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func writeDownloadRelease(t *testing.T, dir, version string, data []byte) desktopRelease {
	t.Helper()
	sum := sha256.Sum256(data)
	release := desktopRelease{Version: version, SourceSHA: strings.Repeat("a", 40), Filename: desktopFilename, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	raw, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, desktopFilename), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "desktop-manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return release
}

func readDownloadRelease(t *testing.T, handler http.Handler) desktopRelease {
	t.Helper()
	w := request(handler, "GET", "/api/v1/user/releases", nil, "", "")
	var body struct {
		Releases []desktopRelease `json:"releases"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Releases) != 1 {
		t.Fatalf("invalid release response: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("release metadata must not be cached")
	}
	return body.Releases[0]
}

func TestPortalDownloadUsesContentHashURLAndCachePolicies(t *testing.T) {
	s, _ := newServer(t)
	s.opts.ArtifactDir = t.TempDir()
	data := []byte("PK-version-one")
	written := writeDownloadRelease(t, s.opts.ArtifactDir, "4.2.0", data)
	handler := s.PublicHandler()
	release := readDownloadRelease(t, handler)
	path := "/downloads/" + written.SHA256 + "/" + desktopFilename
	if release.URL != s.opts.PublicURL+path {
		t.Fatalf("download URL is not addressed by content: %s", release.URL)
	}
	w := request(handler, "GET", "/api/v1/user/download", nil, "", "")
	var info desktopRelease
	if json.Unmarshal(w.Body.Bytes(), &info) != nil || info.URL != release.URL {
		t.Fatal("download info disagrees with releases")
	}
	for _, target := range []string{path, "/downloads/" + desktopFilename} {
		w = request(handler, "GET", target, nil, "", "")
		if w.Code != 200 || w.Body.String() != string(data) || w.Header().Get("ETag") != `"`+written.SHA256+`"` {
			t.Fatalf("download failed: %s (%d)", target, w.Code)
		}
		cache := "no-store"
		if target == path {
			cache = "public, max-age=31536000, immutable"
		}
		if w.Header().Get("Cache-Control") != cache || w.Header().Get("CDN-Cache-Control") != cache {
			t.Fatalf("wrong cache policy for %s: %v", target, w.Header())
		}
		w = request(handler, "HEAD", target, nil, "", "")
		if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "14" || w.Header().Get("Cache-Control") != cache {
			t.Fatalf("HEAD download metadata failed: %s %v", target, w.Header())
		}
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set("Range", "bytes=0-1")
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 206 || w.Body.String() != "PK" || w.Header().Get("Cache-Control") != cache {
			t.Fatalf("range download failed: %s", target)
		}
	}
	for _, invalid := range []string{strings.Repeat("0", 64), "invalid", strings.ToUpper(written.SHA256)} {
		w = request(handler, "GET", "/downloads/"+invalid+"/"+desktopFilename, nil, "", "")
		if w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
			t.Fatalf("wrong hash accepted or cached: %s %d %v", invalid, w.Code, w.Header())
		}
	}
}

func TestPortalDownloadReleaseChangeNeverServesNewBytesAtOldURL(t *testing.T) {
	s, _ := newServer(t)
	s.opts.ArtifactDir = t.TempDir()
	handler := s.PublicHandler()
	writeDownloadRelease(t, s.opts.ArtifactDir, "4.1.0", []byte("PK-version-one"))
	previous := readDownloadRelease(t, handler)
	oldURL, _ := url.Parse(previous.URL)
	data := []byte("PK-version-two") // Deliberately the same size as the previous package.
	writeDownloadRelease(t, s.opts.ArtifactDir, "4.2.0", data)
	current := readDownloadRelease(t, handler)
	if current.URL == previous.URL || current.SHA256 == previous.SHA256 {
		t.Fatal("release replacement reused the CDN cache key")
	}
	w := request(handler, "GET", oldURL.Path, nil, "", "")
	if w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), string(data)) {
		t.Fatal("old content URL served or cached the replacement package")
	}
	currentURL, _ := url.Parse(current.URL)
	for _, target := range []string{currentURL.Path, "/downloads/" + desktopFilename} {
		w = request(handler, "GET", target, nil, "", "")
		if w.Code != 200 || w.Body.String() != string(data) {
			t.Fatalf("new release inaccessible via %s", target)
		}
	}
}

func TestPortalDownloadRechecksOpenedFileAfterCachedVerification(t *testing.T) {
	s, _ := newServer(t)
	s.opts.ArtifactDir = t.TempDir()
	handler := s.PublicHandler()
	writeDownloadRelease(t, s.opts.ArtifactDir, "4.2.0", []byte("PK-version-one"))
	release := readDownloadRelease(t, handler) // Populates the verified metadata cache.
	path := filepath.Join(s.opts.ArtifactDir, desktopFilename)
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("PK-version-two"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	// Preserving size and mtime simulates a stale cached verification. The bytes
	// on the actual download handle must still match its advertised hash.
	u, _ := url.Parse(release.URL)
	for _, target := range []string{u.Path, "/downloads/" + desktopFilename} {
		w := request(handler, "GET", target, nil, "", "")
		if w.Code != 503 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
			t.Fatalf("unverified bytes could reach a CDN cache: %d %v", w.Code, w.Header())
		}
	}
}

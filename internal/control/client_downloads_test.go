package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClientInstallersAndVerifiedDownloads(t *testing.T) {
	dir := t.TempDir()
	s := &Server{opts: Options{ArtifactDir: dir, PublicURL: "https://hqip.example"}}
	var releases []clientDelivery
	for platform, name := range map[string]string{"windows-amd64": "HQIP-Windows-x64-Setup.exe", "android-arm64": "HQIP-Android-arm64.apk", "android-amd64": "HQIP-Android-x64.apk"} {
		data := []byte("verified:" + name)
		hash := sha256.Sum256(data)
		r := clientDelivery{Platform: platform, Filename: name, Version: "5.0.0-preview.17", Sequence: 5000017, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}
		path := filepath.Join(dir, "godot", r.SHA256, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	write := func(dirty bool) {
		raw, _ := json.Marshal(map[string]any{"sourceSHA": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "dirty": dirty, "releases": releases})
		if err := os.WriteFile(filepath.Join(dir, "godot-downloads.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(false)
	rr := httptest.NewRecorder()
	s.clientInstallers(rr, httptest.NewRequest("GET", "/api/v1/user/installers", nil))
	if rr.Code != 200 {
		t.Fatalf("catalog: %d %s", rr.Code, rr.Body.String())
	}
	for _, item := range releases {
		r := httptest.NewRequest("GET", "/downloads/client/"+item.SHA256+"/"+item.Filename, nil)
		r.SetPathValue("sha256", item.SHA256)
		r.SetPathValue("filename", item.Filename)
		r.Header.Set("Range", "bytes=0-7")
		rr = httptest.NewRecorder()
		s.clientInstallerDownload(rr, r)
		if rr.Code != 206 || rr.Body.String() != "verified" {
			t.Fatalf("range: %d %s", rr.Code, rr.Body.String())
		}
		data := make([]byte, item.Size)
		if err := os.WriteFile(filepath.Join(dir, "godot", item.SHA256, item.Filename), data, 0600); err != nil {
			t.Fatal(err)
		}
		rr = httptest.NewRecorder()
		s.clientInstallerDownload(rr, r)
		if rr.Code != 503 || rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("tampered package was served or cached")
		}
		r.SetPathValue("sha256", "../outside")
		rr = httptest.NewRecorder()
		s.clientInstallerDownload(rr, r)
		if rr.Code != 404 {
			t.Fatal("unknown download path was accepted")
		}
	}
	write(true)
	rr = httptest.NewRecorder()
	s.clientInstallers(rr, httptest.NewRequest("GET", "/api/v1/user/installers", nil))
	if rr.Code != 503 {
		t.Fatal("dirty release was published")
	}
}

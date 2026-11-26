package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

// Godot releases are additive; existing Wails metadata and API v3 sessions keep
// their paths and behavior. No release data is stored in the business database.
func (s *Server) godotReleaseCatalog() (clientrelease.Catalog, error) {
	var catalog clientrelease.Catalog
	file, err := os.Open(filepath.Join(s.opts.ArtifactDir, "godot-releases.json"))
	if err != nil {
		return catalog, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) > 65536 {
		return catalog, errors.New("invalid release catalog")
	}
	if json.Unmarshal(raw, &catalog) != nil || len(catalog.Releases) > 16 {
		return catalog, errors.New("invalid release catalog")
	}
	for _, envelope := range catalog.Releases {
		if _, err = envelope.Decode(); err != nil {
			return catalog, err
		}
	}
	return catalog, nil
}
func (s *Server) godotReleases(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("client") == "flutter" {
		s.flutterReleases(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	catalog, err := s.godotReleaseCatalog()
	if errors.Is(err, os.ErrNotExist) {
		reply(w, 200, clientrelease.Catalog{Releases: []clientrelease.Envelope{}})
		return
	}
	if err != nil {
		fail(w, 503, "更新渠道暂不可用")
		return
	}
	reply(w, 200, catalog)
}
func (s *Server) godotDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	catalog, err := s.godotReleaseCatalog()
	if err != nil {
		fail(w, 404, "更新包不存在")
		return
	}
	for _, envelope := range catalog.Releases {
		release, err := envelope.Decode()
		if err != nil {
			continue
		}
		if release.SHA256 != r.PathValue("sha256") || release.Filename != r.PathValue("filename") {
			continue
		}
		file, err := clientrelease.VerifyFile(release.ArtifactPath(s.opts.ArtifactDir), release)
		if err != nil {
			fail(w, 503, "更新包校验失败")
			return
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil {
			fail(w, 503, "更新包暂不可用")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("ETag", "\""+release.SHA256+"\"")
		w.Header().Set("Content-Disposition", "attachment; filename="+release.Filename)
		w.Header().Set("Content-Type", "application/octet-stream")
		serveVerifiedDownload(w, r, release.Filename, stat.ModTime(), file)
		return
	}
	fail(w, 404, "该更新包已不可用，请稍后重试")
}

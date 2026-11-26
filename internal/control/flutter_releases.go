package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/KeepS13/HQIP/internal/flutterrelease"
)

func (s *Server) flutterCatalog() (flutterrelease.Catalog, error) {
	var c flutterrelease.Catalog
	f, err := os.Open(filepath.Join(s.opts.ArtifactDir, "flutter-preview-releases.json"))
	if err != nil {
		return c, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &c) != nil || len(c.Releases) > 16 {
		return c, errors.New("invalid preview catalog")
	}
	for _, e := range c.Releases {
		if _, err = e.Decode(); err != nil {
			return c, err
		}
	}
	return c, nil
}
func (s *Server) flutterReleases(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("channel") != "preview" {
		fail(w, 404, "预览渠道不存在")
		return
	}
	c, err := s.flutterCatalog()
	if errors.Is(err, os.ErrNotExist) {
		reply(w, 200, flutterrelease.Catalog{Releases: []flutterrelease.Envelope{}})
		return
	}
	if err != nil {
		fail(w, 503, "预览渠道暂不可用")
		return
	}
	reply(w, 200, c)
}
func (s *Server) flutterInstallers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("channel") != "preview" {
		fail(w, 404, "预览渠道不存在")
		return
	}
	c, err := s.flutterCatalog()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fail(w, 503, "预览安装包暂不可用")
		return
	}
	items := []clientDelivery{}
	for _, e := range c.Releases {
		v, _ := e.Decode()
		if v.Kind != "installer" {
			continue
		}
		items = append(items, clientDelivery{Platform: v.Platform, Name: "HQIP Preview", Filename: v.Filename, Version: v.Version, Sequence: v.Sequence, SHA256: v.SHA256, Size: v.Size, URL: s.opts.PublicURL + v.DownloadPath()})
	}
	reply(w, 200, map[string]any{"releases": items})
}
func (s *Server) flutterDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := s.flutterCatalog()
	if err != nil {
		fail(w, 404, "预览包不存在")
		return
	}
	for _, e := range c.Releases {
		v, _ := e.Decode()
		if v.SHA256 != r.PathValue("sha256") || v.Filename != r.PathValue("filename") {
			continue
		}
		f, err := flutterrelease.VerifyFile(v.ArtifactPath(s.opts.ArtifactDir), v)
		if err != nil {
			fail(w, 503, "预览包校验失败")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			fail(w, 503, "预览包暂不可用")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("ETag", `"`+v.SHA256+`"`)
		w.Header().Set("Content-Disposition", "attachment; filename="+v.Filename)
		serveVerifiedDownload(w, r, v.Filename, info.ModTime(), f)
		return
	}
	fail(w, 404, "预览包不存在")
}

package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Installer metadata is separate from the legacy Wails update contract, whose
// consumer only accepts ZIP filenames. Release files are immutable deployment
// data; they never enter the business database.
type clientDelivery struct {
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Filename string `json:"filename"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

func (s *Server) clientDeliveries() ([]clientDelivery, error) {
	file, err := os.Open(filepath.Join(s.opts.ArtifactDir, "godot-downloads.json"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	var manifest struct {
		SourceSHA string           `json:"sourceSHA"`
		Dirty     *bool            `json:"dirty"`
		Releases  []clientDelivery `json:"releases"`
	}
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &manifest) != nil || !hex40.MatchString(manifest.SourceSHA) || manifest.Dirty == nil || *manifest.Dirty || len(manifest.Releases) != 3 {
		return nil, errors.New("invalid installer catalog")
	}
	expected := map[string]string{"windows-amd64": "HQIP-Windows-x64-Setup.exe", "android-arm64": "HQIP-Android-arm64.apk", "android-amd64": "HQIP-Android-x64.apk"}
	seen := map[string]bool{}
	for i := range manifest.Releases {
		r := &manifest.Releases[i]
		if expected[r.Platform] == "" || r.Filename != expected[r.Platform] || seen[r.Platform] || !hex64.MatchString(r.SHA256) || r.Size <= 0 || r.Size > 500<<20 || r.Sequence <= 0 || r.Version == "" || len(r.Version) > 80 {
			return nil, errors.New("invalid installer entry")
		}
		seen[r.Platform] = true
		r.URL = s.opts.PublicURL + "/downloads/client/" + r.SHA256 + "/" + r.Filename
	}
	return manifest.Releases, nil
}

func (s *Server) clientInstallers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	releases, err := s.clientDeliveries()
	if errors.Is(err, os.ErrNotExist) {
		reply(w, 200, map[string]any{"releases": []clientDelivery{}})
		return
	}
	if err != nil {
		fail(w, 503, "客户端安装包暂不可用")
		return
	}
	reply(w, 200, map[string]any{"releases": releases})
}

func (s *Server) clientInstallerDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	releases, err := s.clientDeliveries()
	if err != nil {
		fail(w, 503, "客户端安装包暂不可用")
		return
	}
	for _, release := range releases {
		if release.SHA256 != r.PathValue("sha256") || release.Filename != r.PathValue("filename") {
			continue
		}
		path := filepath.Join(s.opts.ArtifactDir, "godot", release.SHA256, release.Filename)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != release.Size {
			fail(w, 503, "安装包校验失败")
			return
		}
		file, err := os.Open(path)
		if err != nil {
			fail(w, 503, "安装包暂不可用")
			return
		}
		defer file.Close()
		// Hash and serve the same handle; replacing a file cannot change the bytes
		// served under an already cached content-addressed URL. Ranges are supported.
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(file, (500<<20)+1))
		if err != nil || n != release.Size || hex.EncodeToString(h.Sum(nil)) != release.SHA256 {
			fail(w, 503, "安装包校验失败")
			return
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			fail(w, 503, "安装包暂不可用")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename="+release.Filename)
		w.Header().Set("ETag", "\""+release.SHA256+"\"")
		http.ServeContent(w, r, release.Filename, info.ModTime(), file)
		return
	}
	fail(w, 404, "该安装包已不可用，请刷新下载页面")
}

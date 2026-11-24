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
	"regexp"
	"sync"
	"time"
)

const desktopFilename = "HQIP-Windows-x64.zip"

type desktopRelease struct {
	Dirty     bool   `json:"dirty"`
	Version   string `json:"version"`
	SourceSHA string `json:"sourceSHA"`
	Filename  string `json:"filename"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Platform  string `json:"platform"`
	URL       string `json:"url"`
}
type verifiedDesktop struct {
	fileSize int64
	fileTime time.Time
	manifest string
	release  desktopRelease
}

var desktopCache struct {
	sync.Mutex
	files map[string]verifiedDesktop
}
var hex40 = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var hex64 = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

func (s *Server) desktopRelease() (desktopRelease, error) {
	var release desktopRelease
	path := filepath.Join(s.opts.ArtifactDir, desktopFilename)
	stat, err := os.Lstat(path)
	if err != nil {
		return release, err
	}
	if !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > 500<<20 {
		return release, errors.New("invalid desktop artifact")
	}
	mf, err := os.Open(filepath.Join(s.opts.ArtifactDir, "desktop-manifest.json"))
	if err != nil {
		return release, err
	}
	raw, err := io.ReadAll(io.LimitReader(mf, 16385))
	_ = mf.Close()
	if err != nil || len(raw) > 16384 {
		return release, errors.New("invalid desktop manifest")
	}
	desktopCache.Lock()
	defer desktopCache.Unlock()
	if cached, ok := desktopCache.files[path]; ok && cached.fileSize == stat.Size() && cached.fileTime.Equal(stat.ModTime()) && cached.manifest == string(raw) {
		return cached.release, nil
	}
	if json.Unmarshal(raw, &release) != nil || release.Dirty || release.Filename != desktopFilename || !hex40.MatchString(release.SourceSHA) || !hex64.MatchString(release.SHA256) || release.Version == "" || len(release.Version) > 80 || release.Size != stat.Size() {
		return release, errors.New("invalid desktop manifest")
	}
	f, err := os.Open(path)
	if err != nil {
		return release, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	_ = f.Close()
	if err != nil || hex.EncodeToString(h.Sum(nil)) != release.SHA256 {
		return release, errors.New("desktop artifact checksum mismatch")
	}
	release.Platform = "windows-amd64"
	release.URL = s.opts.PublicURL + "/downloads/" + release.SHA256 + "/" + desktopFilename
	if desktopCache.files == nil {
		desktopCache.files = map[string]verifiedDesktop{}
	}
	desktopCache.files[path] = verifiedDesktop{stat.Size(), stat.ModTime(), string(raw), release}
	return release, nil
}
func (s *Server) portalReleases(w http.ResponseWriter, r *http.Request) {
	release, err := s.desktopRelease()
	if errors.Is(err, os.ErrNotExist) {
		reply(w, 200, map[string]any{"releases": []desktopRelease{}})
		return
	}
	if err != nil {
		fail(w, 503, "客户端发布包暂不可用")
		return
	}
	reply(w, 200, map[string]any{"releases": []desktopRelease{release}})
}
func (s *Server) portalDownloadInfo(w http.ResponseWriter, r *http.Request) {
	release, err := s.desktopRelease()
	if err != nil {
		fail(w, 404, "客户端发布包暂不可用")
		return
	}
	reply(w, 200, release)
}
func (s *Server) portalDownload(w http.ResponseWriter, r *http.Request) {
	// The fixed legacy URL and failed versioned requests must never populate a
	// reusable cache entry. Only verified bytes get immutable cache headers below.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("CDN-Cache-Control", "no-store")
	release, err := s.desktopRelease()
	if err != nil {
		fail(w, 404, "客户端发布包暂不可用")
		return
	}
	requestedHash := r.PathValue("sha256")
	if requestedHash != "" && requestedHash != release.SHA256 {
		fail(w, 404, "该版本发布包已不可用，请刷新下载页面")
		return
	}
	f, err := os.Open(filepath.Join(s.opts.ArtifactDir, desktopFilename))
	if err != nil {
		fail(w, 404, "客户端发布包暂不可用")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() != release.Size {
		fail(w, 503, "客户端发布包正在更新")
		return
	}
	// The artifact can be replaced between desktopRelease and Open, including
	// with a same-sized ZIP. Verify this handle, then serve that same handle so
	// an atomic release replacement cannot publish new bytes at an old hash URL.
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != release.SHA256 {
		fail(w, 503, "客户端发布包正在更新")
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		fail(w, 503, "客户端发布包暂不可用")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+desktopFilename)
	w.Header().Set("ETag", "\""+release.SHA256+"\"")
	if requestedHash != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("CDN-Cache-Control", "public, max-age=31536000, immutable")
	}
	serveVerifiedDownload(w, r, desktopFilename, stat.ModTime(), f)
}

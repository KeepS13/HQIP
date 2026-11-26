package flutterrelease

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Files []File `json:"files"`
}

func safePath(name string) bool {
	if name == "" || len(name) > 240 || strings.ContainsAny(name, `\:`+"\x00\r\n") || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, " .") != part || part == "." || part == ".." {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
			return false
		}
	}
	return true
}
func ParseManifest(raw []byte, hash string) (Manifest, error) {
	var m Manifest
	sum := sha256.Sum256(raw)
	if len(raw) > 1<<20 || hex.EncodeToString(sum[:]) != hash || json.Unmarshal(raw, &m) != nil || len(m.Files) == 0 || len(m.Files) > 4000 {
		return m, errors.New("invalid payload manifest")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range m.Files {
		key := strings.ToLower(f.Path)
		if !safePath(f.Path) || key == "payload.json" || key == "release.json" || key == ".startup-ready" || seen[key] || f.Size < 0 || f.Size > 300<<20 || !digestPattern.MatchString(f.SHA256) {
			return m, errors.New("invalid payload entry")
		}
		seen[key] = true
		total += f.Size
	}
	if total > 700<<20 {
		return m, errors.New("payload too large")
	}
	for _, name := range []string{"hqip-ui.exe", "hqip-runtime.exe", "flutter_windows.dll", "data/icudtl.dat", "sing-box.exe", "hqip-helper.exe", "libcronet.dll"} {
		if !seen[name] {
			return m, errors.New("incomplete Windows payload: " + name)
		}
	}
	return m, nil
}
func Extract(archive, destination string, r Release) error {
	if r.Platform != "windows-amd64" || r.Kind != "update" {
		return errors.New("wrong package type")
	}
	f, err := VerifyFile(archive, r)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := zip.NewReader(f, r.Size)
	if err != nil {
		return err
	}
	entries := map[string]*zip.File{}
	for _, entry := range z.File {
		if !safePath(entry.Name) || !entry.Mode().IsRegular() || entries[entry.Name] != nil {
			return errors.New("invalid archive layout")
		}
		entries[entry.Name] = entry
	}
	manifest := entries["payload.json"]
	if manifest == nil || manifest.UncompressedSize64 > 1<<20 {
		return errors.New("missing payload manifest")
	}
	in, err := manifest.Open()
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(in, (1<<20)+1))
	in.Close()
	if err != nil {
		return err
	}
	m, err := ParseManifest(raw, r.ManifestSHA256)
	if err != nil {
		return err
	}
	if len(entries) != len(m.Files)+1 {
		return errors.New("unexpected archive entry")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(destination)
		}
	}()
	for _, item := range m.Files {
		entry := entries[item.Path]
		if entry == nil || entry.UncompressedSize64 != uint64(item.Size) {
			return errors.New("payload entry size mismatch")
		}
		target := filepath.Join(destination, filepath.FromSlash(item.Path))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err = extractFile(entry, target, item); err != nil {
			return err
		}
	}
	if err = os.WriteFile(filepath.Join(destination, "payload.json"), raw, 0600); err != nil {
		return err
	}
	success = true
	return nil
}
func extractFile(entry *zip.File, target string, item File) error {
	in, err := entry.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, item.Size+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
		return errors.New("payload checksum mismatch")
	}
	return nil
}
func VerifyTree(dir string, r Release) error {
	if err := r.Validate(); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		return err
	}
	m, err := ParseManifest(raw, r.ManifestSHA256)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"payload.json": true, ".startup-ready": true}
	for _, item := range m.Files {
		allowed[filepath.FromSlash(item.Path)] = true
	}
	if err = filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("linked payload entry")
		}
		if !entry.IsDir() {
			relative, err := filepath.Rel(dir, name)
			if err != nil || !allowed[relative] {
				return errors.New("unlisted payload file")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, item := range m.Files {
		name := filepath.Join(dir, filepath.FromSlash(item.Path))
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != item.Size {
			return errors.New("changed payload file")
		}
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, io.LimitReader(f, item.Size+1))
		f.Close()
		if err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
			return errors.New("changed payload checksum")
		}
	}
	return nil
}
func Stage(root, archive string, r Release, e Envelope) error {
	versions := filepath.Join(root, "versions")
	if err := os.MkdirAll(versions, 0700); err != nil {
		return err
	}
	destination := filepath.Join(versions, r.SHA256)
	if _, err := os.Stat(destination); os.IsNotExist(err) {
		temp, err := os.MkdirTemp(versions, ".prepare-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		payload := filepath.Join(temp, "payload")
		if err = Extract(archive, payload, r); err != nil {
			return err
		}
		if err = os.Rename(payload, destination); err != nil {
			return err
		}
	}
	if err := VerifyTree(destination, r); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return clientrelease.WriteAtomic(filepath.Join(root, "pending.json"), raw)
}

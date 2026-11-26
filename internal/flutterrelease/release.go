// Package flutterrelease is the isolated, signed HQIP Preview distribution contract.
package flutterrelease

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const Product = "hqip-flutter"
const Channel = "preview"
const MaxPackageSize int64 = 500 << 20

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Release struct {
	Schema         int    `json:"schema"`
	Product        string `json:"product"`
	Channel        string `json:"channel"`
	Version        string `json:"version"`
	Sequence       int64  `json:"sequence"`
	Platform       string `json:"platform"`
	Kind           string `json:"kind"`
	Filename       string `json:"filename"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	ManifestSHA256 string `json:"manifestSha256,omitempty"`
}
type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}
type Catalog struct {
	Releases []Envelope `json:"releases"`
}

func Filename(platform, kind string) string {
	if kind != "update" && kind != "installer" {
		return ""
	}
	switch platform {
	case "windows-amd64":
		if kind == "installer" {
			return "HQIP-Preview-Windows-x64-Setup.exe"
		}
		return "HQIP-Preview-Windows-x64.zip"
	case "android-arm64":
		return "HQIP-Preview-Android-arm64.apk"
	case "android-amd64":
		return "HQIP-Preview-Android-x64.apk"
	}
	return ""
}
func (r Release) Validate() error {
	if r.Schema != 2 || r.Product != Product || r.Channel != Channel || r.Sequence <= 0 || r.Version == "" || len(r.Version) > 80 || Filename(r.Platform, r.Kind) == "" || r.Filename != Filename(r.Platform, r.Kind) || !digestPattern.MatchString(r.SHA256) || r.Size <= 0 || r.Size > MaxPackageSize {
		return errors.New("invalid preview release")
	}
	if r.Platform == "windows-amd64" && r.Kind == "update" && !digestPattern.MatchString(r.ManifestSHA256) {
		return errors.New("missing authenticated payload manifest")
	}
	return nil
}
func (r Release) DownloadPath() string {
	return "/downloads/flutter/preview/" + r.SHA256 + "/" + r.Filename
}
func (r Release) ArtifactPath(root string) string {
	return filepath.Join(root, "flutter", "preview", r.SHA256, r.Filename)
}
func (e Envelope) Decode() (Release, error) {
	var r Release
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &r) != nil {
		return r, errors.New("invalid preview envelope")
	}
	return r, r.Validate()
}
func (e Envelope) Verify(public string) (Release, error) {
	key, err := hex.DecodeString(public)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Release{}, errors.New("预览版未配置可信更新签名")
	}
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return Release{}, err
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || !ed25519.Verify(key, raw, sig) {
		return Release{}, errors.New("更新签名验证失败")
	}
	return e.Decode()
}
func Sign(r Release, key ed25519.PrivateKey) (Envelope, error) {
	if err := r.Validate(); err != nil {
		return Envelope{}, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("invalid signing key")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))}, nil
}
func VerifyFile(path string, r Release) (*os.File, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != r.Size {
		return nil, errors.New("invalid package size or type")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, r.Size+1))
	if err != nil || n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		f.Close()
		return nil, errors.New("package checksum mismatch")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

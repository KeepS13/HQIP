// Package clientrelease owns the signed, platform-specific installation contract.
// Signing keys and build provenance live outside the business database.
package clientrelease

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

const MaxPackageSize int64 = 500 << 20

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Release struct {
	Schema   int    `json:"schema"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Filename string `json:"filename"`
	APIMin   int    `json:"apiMin"`
	APIMax   int    `json:"apiMax"`
}
type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}
type Catalog struct {
	Releases []Envelope `json:"releases"`
}

func Filename(platform string) string {
	switch platform {
	case "windows-amd64":
		return "HQIP-Godot-Windows-x64.zip"
	case "android-arm64":
		return "HQIP-Android-arm64.apk"
	case "android-amd64":
		return "HQIP-Android-x64.apk"
	default:
		return ""
	}
}
func (r Release) Validate() error {
	if r.Schema != 1 || r.Sequence <= 0 || r.Version == "" || len(r.Version) > 80 || Filename(r.Platform) == "" || r.Filename != Filename(r.Platform) || !digestPattern.MatchString(r.SHA256) || r.Size <= 0 || r.Size > MaxPackageSize || r.APIMin < 1 || r.APIMax < r.APIMin {
		return errors.New("invalid client release")
	}
	return nil
}
func (r Release) DownloadPath() string { return "/downloads/godot/" + r.SHA256 + "/" + r.Filename }
func (r Release) ArtifactPath(dir string) string {
	return filepath.Join(dir, "godot", r.SHA256, r.Filename)
}

func (e Envelope) Decode() (Release, error) {
	var release Release
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil || len(raw) > 8192 {
		return release, errors.New("invalid release payload")
	}
	if err = json.Unmarshal(raw, &release); err != nil {
		return release, err
	}
	return release, release.Validate()
}
func (e Envelope) Verify(publicKey string) (Release, error) {
	key, err := hex.DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Release{}, errors.New("客户端未配置可信更新签名")
	}
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return Release{}, err
	}
	signature, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || !ed25519.Verify(key, raw, signature) {
		return Release{}, errors.New("更新签名验证失败，已保留当前版本")
	}
	return e.Decode()
}
func Sign(release Release, key ed25519.PrivateKey) (Envelope, error) {
	if err := release.Validate(); err != nil {
		return Envelope{}, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("invalid signing key")
	}
	raw, err := json.Marshal(release)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))}, nil
}

// VerifyFile hashes the same regular file that the caller will consume. The
// returned handle is rewound and owned by the caller, preventing rename races.
func VerifyFile(path string, r Release) (*os.File, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != r.Size {
		return nil, errors.New("invalid update file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, MaxPackageSize+1))
	if err != nil || n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		file.Close()
		return nil, errors.New("更新包校验失败，已保留当前版本")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

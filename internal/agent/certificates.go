package agent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// ValidateCertificateBundle never accepts paths or commands from the controller.
// The full chain, key, hostname, validity and version are checked before any file
// becomes visible to Hysteria's dynamic certificate loader.
func ValidateCertificateBundle(b model.CertificateBundle, domain string, now time.Time) (*x509.Certificate, error) {
	if b.Version < 1 || b.Domain != domain || domain == "" || len(b.CertificatePEM) > 128<<10 || len(b.PrivateKeyPEM) > 32<<10 {
		return nil, errors.New("证书版本、域名或大小无效")
	}
	pair, err := tls.X509KeyPair([]byte(b.CertificatePEM), []byte(b.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("证书或私钥无效、不匹配")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("证书格式无效")
	}
	if leaf.IsCA || leaf.VerifyHostname(domain) != nil || now.Before(leaf.NotBefore) || !now.Add(time.Hour).Before(leaf.NotAfter) {
		return nil, errors.New("证书域名或有效期不符合节点要求")
	}
	if (!b.NotBefore.IsZero() && !b.NotBefore.Equal(leaf.NotBefore)) || (!b.NotAfter.IsZero() && !b.NotAfter.Equal(leaf.NotAfter)) {
		return nil, errors.New("证书时间与发布信息不一致")
	}
	// Validate chain structure and signatures. Public-root trust is additionally
	// checked by the mandatory external TLS proxy probe; the bundle cannot supply
	// a trust store or disable certificate verification.
	previous := leaf
	for _, raw := range pair.Certificate[1:] {
		next, e := x509.ParseCertificate(raw)
		if e != nil || !next.IsCA || previous.CheckSignatureFrom(next) != nil || now.Before(next.NotBefore) || !now.Before(next.NotAfter) {
			return nil, errors.New("证书链无效")
		}
		previous = next
	}
	if len(pair.Certificate) < 2 {
		return nil, errors.New("证书必须包含中间证书链")
	}
	return leaf, nil
}

func atomicPrivateFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".new-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp, path)
}

func (a *Agent) applyCertificate(b model.CertificateBundle) error {
	if a.cfg.CertificateMode != "cloudflare" {
		return errors.New("该节点未启用托管证书")
	}
	leaf, err := ValidateCertificateBundle(b, a.cfg.Domain, a.now())
	if err != nil {
		return a.certificateFailure(err)
	}
	a.cacheMu.RLock()
	status := a.cache.Certificate
	a.cacheMu.RUnlock()
	if status != nil && b.Version < status.Version {
		return nil
	}
	root := filepath.Join(filepath.Dir(a.cfg.StatePath), "certificates")
	version := "v" + strconv.FormatInt(b.Version, 10)
	dir := filepath.Join(root, version)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return a.certificateFailure(errors.New("无法创建证书版本目录"))
	}
	// A version is immutable. Replays may complete a previous partial write, but
	// cannot replace a key already published under the same version.
	for file, want := range map[string]string{"cert.pem": b.CertificatePEM, "key.pem": b.PrivateKeyPEM} {
		p := filepath.Join(dir, file)
		existing, e := os.ReadFile(p)
		if e == nil && string(existing) != want {
			return a.certificateFailure(errors.New("相同证书版本内容发生改变"))
		}
		if e != nil {
			if !errors.Is(e, os.ErrNotExist) {
				return a.certificateFailure(errors.New("读取证书版本失败"))
			}
			if e = atomicPrivateFile(p, []byte(want)); e != nil {
				return a.certificateFailure(errors.New("写入证书版本失败"))
			}
		}
	}
	current := filepath.Join(root, "current")
	target, e := os.Readlink(current)
	if e != nil || target != version {
		link := filepath.Join(root, ".current-"+version)
		_ = os.Remove(link)
		if err = os.Symlink(version, link); err != nil {
			return a.certificateFailure(errors.New("创建原子证书引用失败"))
		}
		if err = os.Rename(link, current); err != nil {
			_ = os.Remove(link)
			return a.certificateFailure(errors.New("切换证书版本失败"))
		}
		if directory, e := os.Open(root); e == nil {
			_ = directory.Sync()
			_ = directory.Close()
		}
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	next := cloneCache(a.cache)
	next.Certificate = &model.CertificateStatus{Version: b.Version, NotAfter: leaf.NotAfter}
	if err = a.ledger.saveCache(next); err != nil {
		return err
	}
	a.cache = next
	// Keep current and the preceding generation for recovery; old unused keys
	// cannot fill disk over repeated renewals. All names are generated locally.
	if entries, e := os.ReadDir(root); e == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "v") {
				continue
			}
			v, e := strconv.ParseInt(strings.TrimPrefix(entry.Name(), "v"), 10, 64)
			if e == nil && v > 0 && v < b.Version-1 {
				_ = os.RemoveAll(filepath.Join(root, entry.Name()))
			}
		}
	}
	return nil
}

func (a *Agent) certificateFailure(reason error) error {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	next := cloneCache(a.cache)
	if next.Certificate == nil {
		next.Certificate = &model.CertificateStatus{}
	}
	next.Certificate.Error = reason.Error()
	if err := a.ledger.saveCache(next); err != nil {
		return err
	}
	a.cache = next
	return reason
}

func (a *Agent) observeManualCertificate() {
	if a.cfg.CertificateMode == "cloudflare" || a.cfg.Domain == "" {
		return
	}
	var latest *x509.Certificate
	base := filepath.Join(filepath.Dir(a.cfg.StatePath), "acme")
	count := 0
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		count++
		if count > 1000 {
			return filepath.SkipAll
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || !(strings.HasSuffix(path, ".crt") || strings.HasSuffix(path, ".pem")) {
			return nil
		}
		info, e := d.Info()
		if e != nil || info.Size() > 128<<10 {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil
		}
		cert, e := x509.ParseCertificate(block.Bytes)
		if e == nil && cert.VerifyHostname(a.cfg.Domain) == nil && (latest == nil || cert.NotAfter.After(latest.NotAfter)) {
			latest = cert
		}
		return nil
	})
	if latest == nil {
		return
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	next := cloneCache(a.cache)
	next.Certificate = &model.CertificateStatus{NotAfter: latest.NotAfter}
	if !a.now().Before(latest.NotAfter) {
		next.Certificate.Error = "节点 HTTP 验证证书已过期"
	}
	if err := a.ledger.saveCache(next); err == nil {
		a.cache = next
	}
}

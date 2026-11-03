package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func testCertificateBundle(t *testing.T, version int64, domain string, at time.Time) model.CertificateBundle {
	t.Helper()
	issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(version + 100), Subject: pkix.Name{CommonName: "Test issuer"}, NotBefore: at.Add(-time.Hour), NotAfter: at.Add(90 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuer, issuer, &issuerKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(version), Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain}, NotBefore: at.Add(-time.Minute).Truncate(time.Second), NotAfter: at.Add(60 * 24 * time.Hour).Truncate(time.Second), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, issuer, &leafKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return model.CertificateBundle{Version: version, Domain: domain, CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER})), PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
}

func TestCertificateValidationRejectsWrongDomainKeyChainAndExpiry(t *testing.T) {
	now := time.Now()
	valid := testCertificateBundle(t, 1, "a.example.com", now)
	if _, err := ValidateCertificateBundle(valid, valid.Domain, now); err != nil {
		t.Fatal(err)
	}
	other := testCertificateBundle(t, 2, "b.example.com", now)
	for name, edit := range map[string]func(*model.CertificateBundle){
		"wrong domain": func(b *model.CertificateBundle) { b.Domain = "b.example.com" },
		"wrong key":    func(b *model.CertificateBundle) { b.PrivateKeyPEM = other.PrivateKeyPEM },
		"wrong chain": func(b *model.CertificateBundle) {
			leaf, _ := pem.Decode([]byte(b.CertificatePEM))
			_, rest := pem.Decode([]byte(other.CertificatePEM))
			b.CertificatePEM = string(pem.EncodeToMemory(leaf)) + string(rest)
		},
		"missing chain": func(b *model.CertificateBundle) {
			leaf, _ := pem.Decode([]byte(b.CertificatePEM))
			b.CertificatePEM = string(pem.EncodeToMemory(leaf))
		},
		"invalid version":       func(b *model.CertificateBundle) { b.Version = 0 },
		"wrong expiry metadata": func(b *model.CertificateBundle) { b.NotAfter = now },
	} {
		t.Run(name, func(t *testing.T) {
			b := valid
			edit(&b)
			if _, err := ValidateCertificateBundle(b, "a.example.com", now); err == nil {
				t.Fatal("invalid certificate accepted")
			}
		})
	}
	if _, err := ValidateCertificateBundle(valid, valid.Domain, valid.NotAfter); err == nil {
		t.Fatal("expired certificate accepted")
	}
}

func TestCertificateAtomicPublicationRejectsReplayMutationAndPreservesGoodCache(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("node Agent certificate publication requires Linux atomic directory-symlink rename")
	}
	a := testAgent(t, "", "")
	a.cfg.CertificateMode = "cloudflare"
	a.cfg.Domain = "a.example.com"
	// Windows may require developer mode or an administrator token for symlinks.
	probe := filepath.Join(t.TempDir(), "symlink")
	if err := os.Symlink("target", probe); err != nil {
		t.Skip("symlink capability unavailable", err)
	}
	first := testCertificateBundle(t, 1, a.cfg.Domain, time.Now())
	if err := a.applyCertificate(first); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(filepath.Dir(a.cfg.StatePath), "certificates", "current")
	before, err := os.ReadFile(filepath.Join(current, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := testCertificateBundle(t, 1, a.cfg.Domain, time.Now())
	if err = a.applyCertificate(mutated); err == nil {
		t.Fatal("same-version certificate replacement accepted")
	}
	after, err := os.ReadFile(filepath.Join(current, "cert.pem"))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed certificate changed effective key pair")
	}
	second := testCertificateBundle(t, 2, a.cfg.Domain, time.Now())
	if err = a.applyCertificate(second); err != nil {
		t.Fatal(err)
	}
	if a.cache.Certificate.Version != 2 || a.cache.Certificate.Error != "" {
		t.Fatal("successful publication did not clear failure")
	}
	if err = a.applyCertificate(first); err != nil {
		t.Fatal(err)
	}
	final, err := os.ReadFile(filepath.Join(current, "cert.pem"))
	if err != nil || string(final) != second.CertificatePEM {
		t.Fatal("late response downgraded certificate")
	}
}

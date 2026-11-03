package store

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/certificates"
	"github.com/KeepS13/HQIP/internal/cloudflare"
	"github.com/KeepS13/HQIP/internal/model"
)

func testCertificate(t *testing.T, domain string, now time.Time) *model.CertificateBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), DNSNames: []string{domain}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return &model.CertificateBundle{Domain: domain, CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))}
}
func TestCloudflareTokenEncryptedMaskedAndVersioned(t *testing.T) {
	s, _ := fixture(t)
	zone := cloudflare.Zone{ID: strings.Repeat("1", 32), Name: "example.com", Status: "active"}
	token := "sensitive-cloudflare-token-123456789"
	settings, err := s.SaveCloudflareSettings(CloudflareSettingsInput{Token: token, DefaultZoneID: zone.ID, Email: "ops@example.com"}, zone)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(settings)
	if strings.Contains(string(raw), token) || !settings.Configured || settings.Version != 1 {
		t.Fatalf("unsafe settings %s", raw)
	}
	var stored string
	if err = s.DB.QueryRow("SELECT token FROM cloudflare_settings").Scan(&stored); err != nil || strings.Contains(stored, token) {
		t.Fatalf("plaintext token persisted %v", err)
	}
	if got, e := s.CloudflareToken(); e != nil || got != token {
		t.Fatal("token decryption failed")
	}
	if _, err = s.SaveCloudflareSettings(CloudflareSettingsInput{DefaultZoneID: zone.ID, Email: "changed@example.com", ExpectedVersion: 0}, zone); !errors.Is(err, ErrConflict) {
		t.Fatal("stale settings accepted")
	}
	if _, err = s.SaveCloudflareSettings(CloudflareSettingsInput{DefaultZoneID: zone.ID, Email: "changed@example.com", ExpectedVersion: 1}, zone); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.CloudflareToken(); got != token {
		t.Fatal("blank token erased credentials")
	}
}
func TestDNSJournalSurvivesRetryAndRejectsChangedOwnership(t *testing.T) {
	s, _ := fixture(t)
	node := newNode(t, s, "dns")
	record := cloudflare.Record{Type: "A", Name: "n-test.example.com", Content: node.PublicIP, TTL: 120, Comment: "HQIP test ownership"}
	intent, err := s.CreateDNSIntent(node.ID, strings.Repeat("1", 32), "example.com", record)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.NodeDNSIntent(node.ID)
	if err != nil || recovered.ID != intent.ID || recovered.Record.Comment != record.Comment {
		t.Fatal("intent not durable")
	}
	changed := record
	changed.ID = strings.Repeat("2", 32)
	changed.Content = "198.51.100.4"
	if err = s.CompleteDNSIntent(intent.ID, changed); !errors.Is(err, cloudflare.ErrChanged) {
		t.Fatal("changed content accepted")
	}
	record.ID = strings.Repeat("2", 32)
	if err = s.CompleteDNSIntent(intent.ID, record); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Node(node.ID)
	if err != nil || updated.Domain != record.Name || updated.CertificateMode != "cloudflare" {
		t.Fatalf("domain not atomically published %+v %v", updated, err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET lifecycle='retiring' WHERE id=?", node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateDNSIntent(node.ID, strings.Repeat("1", 32), "example.com", cloudflare.Record{Type: "TXT"}); !errors.Is(err, ErrConflict) {
		t.Fatal("retiring node accepted new resource")
	}
}
func TestCertificateEncryptionVersionsScopeAndRetirement(t *testing.T) {
	s, now := fixture(t)
	a := newNode(t, s, "cert-a")
	b := newNode(t, s, "cert-b")
	if _, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.certificateMode','cloudflare') WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	bundle := testCertificate(t, a.Domain, *now)
	if err := s.PublishCertificate(a.ID, bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Version != 1 {
		t.Fatal("first version incorrect")
	}
	var stored string
	if err := s.DB.QueryRow("SELECT bundle FROM certificate_versions WHERE node_id=?", a.ID).Scan(&stored); err != nil || strings.Contains(stored, "PRIVATE KEY") {
		t.Fatal("unencrypted certificate")
	}
	got, err := s.CertificateBundle(a.ID)
	if err != nil || got.PrivateKeyPEM != bundle.PrivateKeyPEM {
		t.Fatal("bundle read failed")
	}
	if other, err := s.CertificateBundle(b.ID); err != nil || other != nil {
		t.Fatal("cross-node certificate leakage")
	}
	if err = s.RecordCertificateStatus(a.ID, model.CertificateStatus{Version: 2, NotAfter: bundle.NotAfter}); err == nil {
		t.Fatal("unissued version accepted")
	}
	if err = s.RecordCertificateStatus(a.ID, model.CertificateStatus{Version: 1, NotAfter: bundle.NotAfter.Add(time.Hour)}); err == nil {
		t.Fatal("incorrect expiry accepted")
	}
	if err = s.RecordCertificateStatus(a.ID, model.CertificateStatus{Version: 1, NotAfter: bundle.NotAfter}); err != nil {
		t.Fatal(err)
	}
	status, err := s.CertificateStatus(a.ID)
	if err != nil || status.AppliedVersion != 1 || status.Warning != "" {
		t.Fatalf("invalid status %+v %v", status, err)
	}
	*now = now.Add(61 * 24 * time.Hour)
	if !s.CertificateRenewalDue(a.ID) {
		t.Fatal("renewal not due")
	}
	if err = s.CertificateFailure(a.ID, a.Domain, "safe failure"); err != nil {
		t.Fatal(err)
	}
	if s.CertificateRenewalDue(a.ID) {
		t.Fatal("failure backoff missing")
	}
	status, _ = s.CertificateStatus(a.ID)
	if !status.NextAttempt.Equal(now.Add(15 * time.Minute)) {
		t.Fatal("first backoff wrong")
	}
	next := testCertificate(t, a.Domain, *now)
	if err = s.PublishCertificate(a.ID, next); err != nil {
		t.Fatal(err)
	}
	if next.Version != 2 {
		t.Fatal("version did not increase")
	}
	var versions int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM certificate_versions WHERE node_id=?", a.ID).Scan(&versions)
	if versions != 2 {
		t.Fatal("immutable old version was overwritten")
	}
	_, _ = s.DB.Exec("UPDATE nodes SET lifecycle='retiring' WHERE id=?", a.ID)
	if err = s.PublishCertificate(a.ID, testCertificate(t, a.Domain, *now)); err == nil {
		t.Fatal("published certificate during retirement")
	}
	account, err := s.CertificateAccountKey(certificates.ProductionDirectory)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CertificateAccountKey(certificates.ProductionDirectory)
	if err != nil || again != account {
		t.Fatal("account key not persistent")
	}
	_ = s.DB.QueryRow("SELECT private_key FROM certificate_accounts").Scan(&stored)
	if strings.Contains(stored, "PRIVATE KEY") {
		t.Fatal("unencrypted account key")
	}
}
func TestCloudflareLeaseRecovery(t *testing.T) {
	s, now := fixture(t)
	token, err := s.AcquireCloudflareLease("node:test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireCloudflareLease("node:test", time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatal("concurrent lease granted")
	}
	s.ReleaseCloudflareLease("node:test", "wrong-owner")
	if _, err = s.AcquireCloudflareLease("node:test", time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong owner released lease")
	}
	*now = now.Add(2 * time.Minute)
	next, err := s.AcquireCloudflareLease("node:test", time.Minute)
	if err != nil || next == token {
		t.Fatal("expired lease not recoverable")
	}
	s.ReleaseCloudflareLease("node:test", token)
	if _, err = s.AcquireCloudflareLease("node:test", time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatal("stale owner released new lease")
	}
}

func TestCertificateModeTransitionIgnoresCandidateAndRestoresHTTPExpiry(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "cert-transition")
	bundle := testCertificate(t, n.Domain, *now)
	if err := s.PublishCertificate(n.ID, bundle); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{Version: bundle.Version, NotAfter: bundle.NotAfter}); err != nil {
		t.Fatal(err)
	}
	status, _ := s.CertificateStatus(n.ID)
	if status.AppliedVersion != 0 {
		t.Fatal("uncommitted candidate shown applied")
	}
	_, _ = s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.certificateMode','cloudflare') WHERE id=?", n.ID)
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{Version: bundle.Version, NotAfter: bundle.NotAfter}); err != nil {
		t.Fatal(err)
	}
	status, _ = s.CertificateStatus(n.ID)
	if status.AppliedVersion != bundle.Version {
		t.Fatal("committed CF not applied")
	}
	httpExpiry := now.Add(45 * 24 * time.Hour)
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{NotAfter: httpExpiry}); err != nil {
		t.Fatal(err)
	}
	status, _ = s.CertificateStatus(n.ID)
	if !status.NotAfter.Equal(bundle.NotAfter) {
		t.Fatal("HTTP candidate overwrote CF expiry")
	}
	_, _ = s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.certificateMode','http') WHERE id=?", n.ID)
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{NotAfter: httpExpiry}); err != nil {
		t.Fatal(err)
	}
	status, _ = s.CertificateStatus(n.ID)
	if status.AppliedVersion != 0 || !status.NotAfter.Equal(httpExpiry) {
		t.Fatal("committed HTTP status retained stale CF expiry")
	}
}

func TestFailedCertificateApplicationKeepsOldVersionAndShowsSanitizedError(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "cert-error")
	_, _ = s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.certificateMode','cloudflare') WHERE id=?", n.ID)
	bundle := testCertificate(t, n.Domain, *now)
	if err := s.PublishCertificate(n.ID, bundle); err != nil {
		t.Fatal(err)
	}
	secretError := "untrusted PEM PRIVATE KEY and arbitrary node text"
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{Error: secretError}); err != nil {
		t.Fatal(err)
	}
	status, _ := s.CertificateStatus(n.ID)
	if status.AppliedVersion != 0 || status.AppliedError == "" || strings.Contains(status.AppliedError, "PRIVATE") {
		t.Fatalf("initial failure lost/unsafe: %+v", status)
	}
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{Version: 1, NotAfter: bundle.NotAfter}); err != nil {
		t.Fatal(err)
	}
	next := testCertificate(t, n.Domain, *now)
	if err := s.PublishCertificate(n.ID, next); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{Version: 1, NotAfter: bundle.NotAfter, Error: secretError}); err != nil {
		t.Fatal(err)
	}
	status, _ = s.CertificateStatus(n.ID)
	if status.AppliedVersion != 1 || status.IssuedVersion != 2 || status.AppliedError == "" {
		t.Fatalf("failed renewal falsely applied or missing error: %+v", status)
	}
	if err := s.RecordCertificateStatus(n.ID, model.CertificateStatus{Version: 2, NotAfter: next.NotAfter}); err != nil {
		t.Fatal(err)
	}
	status, _ = s.CertificateStatus(n.ID)
	if status.AppliedVersion != 2 || status.AppliedError != "" {
		t.Fatal("successful retry did not clear failure")
	}
}

func TestVerifiedCloudflareSettingsRepairReleasesFailureBackoff(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "cf-repair")
	if err := s.CertificateFailure(n.ID, n.Domain, "token missing"); err != nil {
		t.Fatal(err)
	}
	if s.CertificateRenewalDue(n.ID) {
		t.Fatal("initial backoff not set")
	}
	zone := cloudflare.Zone{ID: strings.Repeat("1", 32), Name: "example.com", Status: "active"}
	if _, err := s.SaveCloudflareSettings(CloudflareSettingsInput{Token: "corrected-cloudflare-token-123456", DefaultZoneID: zone.ID, Email: "admin@example.com"}, zone); err != nil {
		t.Fatal(err)
	}
	if !s.CertificateRenewalDue(n.ID) {
		t.Fatal("verified repair cannot retry")
	}
	status, _ := s.CertificateStatus(n.ID)
	if status.Error != "token missing" {
		t.Fatal("last diagnostic lost before successful retry")
	}
}

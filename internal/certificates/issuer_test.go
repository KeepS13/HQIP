package certificates

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type fakeDNS struct{ presented, waited, cleaned bool }

func (d *fakeDNS) Present(_ context.Context, name, value string) (string, error) {
	d.presented = strings.HasPrefix(name, "_acme-challenge.") && len(value) > 20
	return "record", nil
}
func (d *fakeDNS) Wait(context.Context, string, string) error { d.waited = true; return nil }
func (d *fakeDNS) Cleanup(_ context.Context, id string) error { d.cleaned = id == "record"; return nil }
func TestRFC8555DNSOrderAndCleanup(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	var base string
	valid, issued := false, false
	var certPEM string
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", base64.RawURLEncoding.EncodeToString([]byte(time.Now().String())))
		w.Header().Set("Content-Type", "application/json")
		write := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
		order := func() map[string]any {
			status := "ready"
			if issued {
				status = "valid"
			}
			o := map[string]any{"status": status, "authorizations": []string{base + "/auth"}, "identifiers": []any{map[string]string{"type": "dns", "value": "node.example.com"}}, "finalize": base + "/finalize"}
			if issued {
				o["certificate"] = base + "/cert"
			}
			return o
		}
		switch r.URL.Path {
		case "/directory":
			write(200, map[string]string{"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/new-order"})
		case "/nonce":
			w.WriteHeader(200)
		case "/account":
			w.Header().Set("Location", base+"/account/1")
			write(201, map[string]string{"status": "valid"})
		case "/new-order":
			w.Header().Set("Location", base+"/order/1")
			write(201, order())
		case "/order/1":
			write(200, order())
		case "/auth":
			status := "pending"
			if valid {
				status = "valid"
			}
			write(200, map[string]any{"status": status, "identifier": map[string]string{"type": "dns", "value": "node.example.com"}, "challenges": []any{map[string]string{"type": "dns-01", "url": base + "/challenge", "token": "challenge-token", "status": "pending"}}})
		case "/challenge":
			valid = true
			write(200, map[string]string{"type": "dns-01", "url": base + "/challenge", "token": "challenge-token", "status": "valid"})
		case "/finalize":
			var jws struct {
				Payload string `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&jws)
			raw, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
			var payload struct {
				CSR string `json:"csr"`
			}
			_ = json.Unmarshal(raw, &payload)
			der, _ := base64.RawURLEncoding.DecodeString(payload.CSR)
			csr, err := x509.ParseCertificateRequest(der)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: csr.DNSNames, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			signed, err := x509.CreateCertificate(rand.Reader, leaf, ca, csr.PublicKey, caKey)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: signed}))
			issued = true
			write(200, order())
		case "/cert":
			w.Header().Set("Content-Type", "application/pem-certificate-chain")
			_, _ = w.Write([]byte(certPEM))
		default:
			t.Errorf("unexpected ACME URL %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	account, err := NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{}
	bundle, err := (Issuer{Directory: base + "/directory", HTTPClient: server.Client(), Now: func() time.Time { return now }}).Issue(context.Background(), account, "admin@example.com", "node.example.com", dns)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Domain != "node.example.com" || bundle.NotAfter.IsZero() || !dns.presented || !dns.waited || !dns.cleaned {
		t.Fatalf("bad issuance state %+v dns=%+v", bundle, dns)
	}
	wrong := *bundle
	wrong.Domain = "another.example.com"
	if _, err = Validate(&wrong, now); err == nil {
		t.Fatal("wrong node certificate accepted")
	}
	if _, err = Validate(bundle, bundle.NotAfter.Add(time.Second)); err == nil {
		t.Fatal("expired certificate accepted")
	}
	wrong = *bundle
	wrong.PrivateKeyPEM = account
	if _, err = Validate(&wrong, now); err == nil {
		t.Fatal("mismatched key accepted")
	}
}
func TestRenewalAndBackoffBoundaries(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiry := start.Add(90 * 24 * time.Hour)
	if got := RenewalTime(start, expiry); !got.Equal(start.Add(60 * 24 * time.Hour)) {
		t.Fatal(got)
	}
	if RetryDelay(1) != 15*time.Minute || RetryDelay(100) > 24*time.Hour {
		t.Fatal("invalid retry limits")
	}
	for _, days := range []int{14, 7, 3, 0} {
		if ExpiryWarning(expiry.Add(-time.Duration(days)*24*time.Hour), expiry) == "" {
			t.Fatalf("missing %d-day warning", days)
		}
	}
	if ExpiryWarning(start, expiry) != "" {
		t.Fatal("premature expiry warning")
	}
	if _, err := Validate(&model.CertificateBundle{}, start); err == nil {
		t.Fatal("empty certificate accepted")
	}
}

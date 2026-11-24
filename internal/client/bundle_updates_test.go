package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

// A real TLS server exercises catalog, download, persistence and stage boundaries.
// The fixture trusts only its test certificate; production still uses system CAs.
type bundleFixture struct {
	t                 *testing.T
	private           ed25519.PrivateKey
	public            string
	catalog           clientrelease.Catalog
	packageBody       []byte
	status            int
	downloads, stages int
	server            *httptest.Server
	source            string
	dir               string
	onDownload        func()
}

func newBundleFixture(t *testing.T) *bundleFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &bundleFixture{t: t, private: private, public: hex.EncodeToString(public), status: 200, packageBody: []byte("signed apk fixture"), dir: t.TempDir()}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/client/releases" {
			w.WriteHeader(f.status)
			_ = json.NewEncoder(w).Encode(f.catalog)
			return
		}
		f.downloads++
		if f.onDownload != nil {
			f.onDownload()
		}
		_, _ = w.Write(f.packageBody)
	}))
	t.Cleanup(f.server.Close)
	f.source = f.server.URL
	f.publish("5.0.1", 5000002)
	return f
}
func (f *bundleFixture) publish(version string, sequence int64) {
	hash := sha256.Sum256(f.packageBody)
	r := clientrelease.Release{Schema: 1, Version: version, Sequence: sequence, Platform: "android-arm64", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(f.packageBody)), Filename: clientrelease.Filename("android-arm64"), APIMin: 3, APIMax: 3}
	envelope, err := clientrelease.Sign(r, f.private)
	if err != nil {
		f.t.Fatal(err)
	}
	f.catalog = clientrelease.Catalog{Releases: []clientrelease.Envelope{envelope}}
}
func (f *bundleFixture) updater() *BundleUpdater {
	u := NewBundleUpdater(f.dir, "android-arm64", "5.0.0", func() string { return f.source }, func(path string, r clientrelease.Release, e clientrelease.Envelope) error {
		file, err := clientrelease.VerifyFile(path, r)
		if err == nil {
			file.Close()
			f.stages++
		}
		return err
	})
	u.publicKey = f.public
	u.http.Transport = f.server.Client().Transport
	return u
}
func TestBundleUpdateReadyAndReusesVerifiedDownload(t *testing.T) {
	f := newBundleFixture(t)
	u := f.updater()
	for range 2 {
		if err := u.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !u.View().Ready || f.downloads != 1 || f.stages != 1 {
		t.Fatalf("view=%+v downloads=%d stages=%d", u.View(), f.downloads, f.stages)
	}
	// A restarted process stages again, but does not redownload verified bytes.
	if err := f.updater().Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.downloads != 1 || f.stages != 2 {
		t.Fatal("verified cache was not reused")
	}
}
func TestBundleUpdateRejectsUnsafeMetadataAndBytes(t *testing.T) {
	for _, kind := range []string{"signature", "digest", "size", "incompatible", "old-sequence", "redirect", "canceled", "source-changed"} {
		t.Run(kind, func(t *testing.T) {
			f := newBundleFixture(t)
			u := f.updater()
			ctx := context.Background()
			switch kind {
			case "signature":
				f.catalog.Releases[0].Signature = "invalid"
			case "digest":
				f.packageBody = []byte(strings.Repeat("x", len(f.packageBody)))
			case "size":
				f.packageBody = f.packageBody[:3]
			case "incompatible":
				r, _ := f.catalog.Releases[0].Decode()
				r.APIMin = 4
				r.APIMax = 4
				f.catalog.Releases[0], _ = clientrelease.Sign(r, f.private)
			case "old-sequence":
				f.publish("6.0.0", 5000001)
			case "redirect":
				f.status = 302
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "source-changed":
				f.onDownload = func() { f.source = "" }
			}
			err := u.Check(ctx)
			if err == nil && kind != "incompatible" && kind != "old-sequence" {
				t.Fatal("unsafe update accepted")
			}
			if u.View().Ready || f.stages != 0 {
				t.Fatal("unsafe update reached installer")
			}
			partial, _ := filepath.Glob(filepath.Join(f.dir, "*.part"))
			if len(partial) != 0 {
				t.Fatal("partial download retained")
			}
		})
	}
}
func TestBundleUpdateHighWaterSurvivesFailedDownload(t *testing.T) {
	f := newBundleFixture(t)
	f.publish("5.0.2", 5000003)
	f.packageBody = []byte("incomplete")
	if err := f.updater().Check(context.Background()); err == nil {
		t.Fatal("expected download failure")
	}
	f.publish("5.0.1", 5000002)
	u := f.updater()
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.View().Ready || f.stages != 0 || f.downloads != 1 {
		t.Fatal("persisted sequence allowed downgrade")
	}
	f.publish("5.0.3", 5000003) // Same sequence cannot be reassigned to different bytes.
	if err := u.Check(context.Background()); err == nil {
		t.Fatal("conflicting release accepted")
	}
}
func TestBundleUpdateOlderServerAndCorruptHistory(t *testing.T) {
	f := newBundleFixture(t)
	f.status = 404
	u := f.updater()
	if err := u.Check(context.Background()); err != nil || u.View().Ready {
		t.Fatalf("old server: %v", err)
	}
	f.status = 200
	if err := os.WriteFile(filepath.Join(f.dir, "seen-release.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := u.Check(context.Background()); err == nil {
		t.Fatal("corrupt history accepted")
	}
	if f.downloads != 0 {
		t.Fatal("download started despite invalid history")
	}
}

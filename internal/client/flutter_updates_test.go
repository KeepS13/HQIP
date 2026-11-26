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
	"sync/atomic"
	"testing"

	"github.com/KeepS13/HQIP/internal/flutterrelease"
)

func TestFlutterUpdateMeteredSignatureHashAndReplay(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	body := []byte("preview apk")
	sum := sha256.Sum256(body)
	r := flutterrelease.Release{Schema: 2, Product: flutterrelease.Product, Channel: flutterrelease.Channel, Version: "preview-2", Sequence: 2, Platform: "android-arm64", Kind: "update", Filename: flutterrelease.Filename("android-arm64", "update"), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	e, _ := flutterrelease.Sign(r, private)
	catalog := flutterrelease.Catalog{Releases: []flutterrelease.Envelope{e}}
	var downloads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v3/client/releases" {
			if request.URL.Query().Get("client") != "flutter" || request.URL.Query().Get("channel") != "preview" {
				t.Error("legacy channel requested")
			}
			_ = json.NewEncoder(w).Encode(catalog)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	staged := 0
	u := NewFlutterUpdater(t.TempDir(), "android-arm64", func() string { return server.URL }, func() bool { return true }, func(path string, r flutterrelease.Release, _ flutterrelease.Envelope) error {
		f, err := flutterrelease.VerifyFile(path, r)
		if err == nil {
			f.Close()
			staged++
		}
		return err
	})
	u.installed = 1
	u.key = hex.EncodeToString(public)
	u.http.Transport = server.Client().Transport
	if err := u.Check(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 || u.View().Phase != "available" {
		t.Fatal("metered connection downloaded automatically")
	}
	if err := u.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if staged != 1 || !u.View().Ready {
		t.Fatal("verified update was not staged")
	}
	// Same sequence with different bytes is never treated as a new release.
	r.SHA256 = hex.EncodeToString(make([]byte, 32))
	e, _ = flutterrelease.Sign(r, private)
	catalog.Releases = []flutterrelease.Envelope{e}
	if err := u.Check(context.Background(), true); err == nil || u.View().Ready {
		t.Fatal("conflicting release accepted or stale readiness retained")
	}
	// A valid signature from an unrelated key must fail before downloading.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	r.Sequence = 3
	e, _ = flutterrelease.Sign(r, other)
	catalog.Releases = []flutterrelease.Envelope{e}
	if err := u.Check(context.Background(), true); err == nil {
		t.Fatal("untrusted key accepted")
	}
	if downloads.Load() != 1 {
		t.Fatal("untrusted release downloaded")
	}
}

func TestFlutterUpdateNeverStagesCorruptedDownload(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256([]byte("expected"))
	r := flutterrelease.Release{Schema: 2, Product: flutterrelease.Product, Channel: flutterrelease.Channel, Version: "preview-2", Sequence: 2, Platform: "android-arm64", Kind: "update", Filename: flutterrelease.Filename("android-arm64", "update"), SHA256: hex.EncodeToString(sum[:]), Size: 8}
	e, _ := flutterrelease.Sign(r, private)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v3/client/releases" {
			_ = json.NewEncoder(w).Encode(flutterrelease.Catalog{Releases: []flutterrelease.Envelope{e}})
			return
		}
		_, _ = w.Write([]byte("tampered"))
	}))
	defer server.Close()
	u := NewFlutterUpdater(t.TempDir(), "android-arm64", func() string { return server.URL }, nil, func(string, flutterrelease.Release, flutterrelease.Envelope) error {
		t.Fatal("corrupt payload staged")
		return nil
	})
	u.installed = 1
	u.key = hex.EncodeToString(public)
	u.http.Transport = server.Client().Transport
	if err := u.Check(context.Background(), true); err == nil || u.View().Ready {
		t.Fatal("corrupt payload accepted")
	}
}

package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

// Exercise real truncated TLS responses and resumed byte offsets. No account,
// proxy configuration or installed application is used by this transfer test.
func TestBundleDownloadResumesInterruptedResponse(t *testing.T) {
	for _, mode := range []string{"resume", "range-ignored", "wrong-range", "corrupt-resume"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte(strings.Repeat("signed package bytes", 8))
			digest := sha256.Sum256(body)
			release := clientrelease.Release{Schema: 1, Version: "1.0.0", Sequence: 5000020, Platform: "android-arm64", Filename: clientrelease.Filename("android-arm64"), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), APIMin: 3, APIMax: 3}
			var mu sync.Mutex
			var ranges []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				ranges = append(ranges, r.Header.Get("Range"))
				attempt := len(ranges)
				mu.Unlock()
				if attempt == 1 {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
					_, _ = w.Write(body[:17]) // net/http closes this deliberately short response.
					return
				}
				if mode == "range-ignored" {
					_, _ = w.Write(body)
					return
				}
				start := 17
				if mode == "wrong-range" {
					start++
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
				w.WriteHeader(http.StatusPartialContent)
				remainder := append([]byte(nil), body[start:]...)
				if mode == "corrupt-resume" {
					remainder[0] ^= 1
				}
				_, _ = w.Write(remainder)
			}))
			defer server.Close()
			updater := &BundleUpdater{http: server.Client()}
			path := filepath.Join(t.TempDir(), release.Filename)
			err := updater.download(context.Background(), server.URL, path, release)
			wantSuccess := mode == "resume" || mode == "range-ignored"
			if (err == nil) != wantSuccess {
				t.Fatalf("download error=%v, success=%v", err, wantSuccess)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(ranges) != 2 || ranges[0] != "" || ranges[1] != "bytes=17-" {
				t.Fatalf("unexpected retries: %v", ranges)
			}
			if wantSuccess {
				got, readErr := os.ReadFile(path)
				if readErr != nil || string(got) != string(body) {
					t.Fatalf("resumed bytes differ: %v", readErr)
				}
			} else if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatal("invalid resumed payload was published")
			}
			if _, statErr := os.Stat(path + ".part"); !os.IsNotExist(statErr) {
				t.Fatal("partial file survived completion or rejection")
			}
		})
	}
}

func TestBundleDownloadDoesNotRetryCertificateFailure(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("untrusted HTTPS server reached HTTP handler")
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	updater := &BundleUpdater{http: &http.Client{Transport: &http.Transport{}}}
	defer updater.http.CloseIdleConnections()
	err := updater.download(context.Background(), server.URL, filepath.Join(t.TempDir(), "package.apk"), clientrelease.Release{Size: 10})
	if err == nil || connections.Load() != 1 {
		t.Fatalf("certificate failure was accepted or retried: connections=%d error=%v", connections.Load(), err)
	}
}

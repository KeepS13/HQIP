package outbound_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/outbound"
	"github.com/KeepS13/HQIP/internal/testoutbound"
	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

func TestSessionRevocationClosesIdleSocketsAndFinalIsOnce(t *testing.T) {
	var revoked atomic.Bool
	var finals atomic.Int32
	usage := make(chan [2]int64, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u struct {
			UploadBytes, DownloadBytes int64
			Closed                     bool
		}
		if r.URL.Path != "/admission" {
			_ = json.NewDecoder(r.Body).Decode(&u)
			if u.Closed {
				finals.Add(1)
				usage <- [2]int64{u.UploadBytes, u.DownloadBytes}
			}
		}
		if revoked.Load() && !u.Closed {
			http.Error(w, "revoked", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(nodepolicy.LeaseGrant{LeaseID: "session", UserID: "user", AccountingID: "account", BudgetBytes: 1 << 20, ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer api.Close()
	admission, err := nodepolicy.NewAdmission(nodepolicy.AdmissionConfig{URL: api.URL, Secret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	session, err := outbound.NewSession(context.Background(), admission, "password", "198.51.100.1", testoutbound.Loopback{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	tcp, err := session.TCP(echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	_ = tcp.SetDeadline(time.Now().Add(8 * time.Second))
	payload := []byte("guarded-payload")
	if _, err = tcp.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(tcp, got); err != nil || string(got) != string(payload) {
		t.Fatal("TCP payload", err)
	}
	udp, err := session.UDP()
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udpDone := make(chan error, 1)
	go func() { _, _, err := udp.Receive(); udpDone <- err }()
	revoked.Store(true)
	closed := make(chan error, 1)
	go func() { _, err := tcp.Read(got); closed <- err }()
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("idle TCP survived revocation")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("revocation did not close TCP")
	}
	select {
	case err := <-udpDone:
		if err == nil {
			t.Fatal("idle UDP survived revocation")
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not close UDP")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = session.Close() }()
	}
	wg.Wait()
	if finals.Load() != 1 {
		t.Fatal("duplicate final", finals.Load())
	}
	if counters := <-usage; counters != [2]int64{int64(len(payload)), int64(len(payload))} {
		t.Fatal("payload metered more/less than once", counters)
	}
	if next, err := session.TCP(echo.Addr().String()); err == nil {
		next.Close()
		t.Fatal("revoked session dialed a new target")
	}
}

func TestSessionDeniedReturnsNilClient(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", 403) }))
	defer api.Close()
	a, err := nodepolicy.NewAdmission(nodepolicy.AdmissionConfig{URL: api.URL, Secret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := outbound.NewSession(context.Background(), a, "invalid", "198.51.100.1", testoutbound.Loopback{})
	if err == nil || c != nil {
		t.Fatal("denial returned an unusable typed-nil client", c, err)
	}
}

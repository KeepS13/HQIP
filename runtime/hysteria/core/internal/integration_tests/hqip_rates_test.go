package integration_tests

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/core/v2/server"
)

func TestHQIPRealConnectionsWaitForRateDecreaseAcknowledgment(t *testing.T) {
	var mu sync.Mutex
	firstID, secondID := "", ""
	released := false
	acked := make(chan struct{})
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := ""
		if r.URL.Path == "/admission" {
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			id = req["connectionId"]
			if firstID == "" {
				firstID = id
			} else if secondID == "" {
				secondID = id
			} else {
				w.WriteHeader(409)
				return
			}
		} else {
			id = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/leases/"), "/renew")
			var usage struct {
				AppliedRateVersion int64
				Closed             bool
			}
			_ = json.NewDecoder(r.Body).Decode(&usage)
			if usage.Closed {
				w.WriteHeader(403)
				return
			}
			if id == firstID && usage.AppliedRateVersion == 2 && !released {
				released = true
				close(acked)
			}
		}
		g := server.LeaseGrant{LeaseID: id, UserID: "same-user", AccountingID: "same-device", ExpiresAt: time.Now().Add(30 * time.Second), BudgetBytes: 1 << 20, BandwidthBytesPerSecond: 10000, Revision: 1, RateVersion: 1}
		if secondID != "" {
			if id == firstID {
				g.BandwidthBytesPerSecond = 5000
				g.RateVersion = 2
			} else if !released {
				g.BandwidthBytesPerSecond = 0
				g.RatePending = true
			} else {
				g.BandwidthBytesPerSecond = 5000
				g.RateVersion = 2
			}
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a, err := server.NewAdmission(server.AdmissionConfig{URL: central.URL, Secret: "test-admission-secret-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.NewServer(&server.Config{TLSConfig: serverTLSConfig(), Conn: pc, Authenticator: forbiddenFallback{}, HQIPAdmission: a})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	targetSawSecond := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, e := c.Read(buf)
					if n > 0 {
						if buf[0] == 'b' {
							once.Do(func() { close(targetSawSecond) })
						}
						_, _ = c.Write(buf[:n])
					}
					if e != nil {
						return
					}
				}
			}()
		}
	}()
	makeClient := func() client.Client {
		c, _, e := client.NewClient(&client.Config{ServerAddr: pc.LocalAddr(), Auth: "same-user-password-for-two-real-connections", TLSConfig: client.TLSConfig{InsecureSkipVerify: true}})
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	first := makeClient()
	defer first.Close()
	firstStream, err := first.TCP(echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer firstStream.Close()
	_, _ = firstStream.Write([]byte("a"))
	if _, err = io.ReadFull(firstStream, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	second := makeClient()
	defer second.Close()
	secondStream, err := second.TCP(echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer secondStream.Close()
	_ = secondStream.SetDeadline(time.Now().Add(20 * time.Second))
	_, _ = secondStream.Write([]byte("b"))
	select {
	case <-targetSawSecond:
		t.Fatal("pending new share forwarded before old share ACK")
	case <-time.After(250 * time.Millisecond):
	}
	select {
	case <-acked:
	case <-time.After(16 * time.Second):
		t.Fatal("old rate decrease was never acknowledged")
	}
	buf := make([]byte, 1)
	if _, err = io.ReadFull(secondStream, buf); err != nil || buf[0] != 'b' {
		t.Fatal("pending share did not resume after ACK", err)
	}
	select {
	case <-targetSawSecond:
	default:
		t.Fatal("payload did not reach target")
	}
	// Both real connections remain independently usable after the transition.
	_, _ = firstStream.Write([]byte("a"))
	if _, err = io.ReadFull(firstStream, buf); err != nil {
		t.Fatal("old connection was interrupted", err)
	}
}

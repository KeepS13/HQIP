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

type forbiddenFallback struct{}

func (forbiddenFallback) Authenticate(net.Addr, string, uint64) (bool, string) {
	panic("admission must not fall back to legacy authentication")
}

func TestHQIPRealQUICConnectionsIndependentExpiryTCPAndUDP(t *testing.T) {
	var mu sync.Mutex
	ids := map[string]bool{}
	sequence := 0
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-admission-secret-long-enough" {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path != "/admission" {
			w.WriteHeader(403)
			return
		}
		var body struct {
			ConnectionID string `json:"connectionId"`
			Password     string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Password != "same-password-for-both-connections" {
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		if ids[body.ConnectionID] || len(body.ConnectionID) != 64 {
			t.Error("real connections reused identity")
		}
		ids[body.ConnectionID] = true
		sequence++
		seq := sequence
		mu.Unlock()
		lifetime := 20 * time.Second
		if seq == 1 {
			lifetime = 800 * time.Millisecond
		}
		_ = json.NewEncoder(w).Encode(server.LeaseGrant{LeaseID: body.ConnectionID, UserID: "same-user", AccountingID: "same-device", ExpiresAt: time.Now().Add(lifetime), BudgetBytes: 1 << 20, Revision: 1})
	}))
	defer central.Close()
	admission, err := server.NewAdmission(server.AdmissionConfig{URL: central.URL, Secret: "test-admission-secret-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	pc, addr, err := serverConn()
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.NewServer(&server.Config{TLSConfig: serverTLSConfig(), Conn: pc, Authenticator: forbiddenFallback{}, HQIPAdmission: admission})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()
	makeClient := func() client.Client {
		c, _, err := client.NewClient(&client.Config{ServerAddr: addr, Auth: "same-password-for-both-connections", TLSConfig: client.TLSConfig{InsecureSkipVerify: true}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := makeClient()
	defer first.Close()
	second := makeClient()
	defer second.Close()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
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
	stream, err := second.TCP(echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = stream.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err = io.ReadFull(stream, b); err != nil || string(b) != "hello" {
		t.Fatal("HY2 TCP forwarding failed", err)
	}
	time.Sleep(time.Second)
	if _, err = first.TCP(echo.Addr().String()); err == nil {
		t.Fatal("expired QUIC connection still opens streams")
	}
	if _, err = stream.Write([]byte("alive")); err != nil {
		t.Fatal("closing first connection killed second", err)
	}
	if _, err = io.ReadFull(stream, b); err != nil || string(b) != "alive" {
		t.Fatal("second connection did not survive", err)
	}
	udpEcho, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	go func() {
		p := make([]byte, 65535)
		for {
			n, a, e := udpEcho.ReadFrom(p)
			if e != nil {
				return
			}
			_, _ = udpEcho.WriteTo(p[:n], a)
		}
	}()
	u, err := second.UDP()
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if err = u.Send([]byte("udp-compatible"), udpEcho.LocalAddr().String()); err != nil {
		t.Fatal(err)
	}
	got, _, err := u.Receive()
	if err != nil || !strings.EqualFold(string(got), "udp-compatible") {
		t.Fatal("HY2 UDP forwarding failed", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 {
		t.Fatalf("expected 2 real connections, got %d", len(ids))
	}
}

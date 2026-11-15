package client

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestTLSFallbackEscapesEstablishedTCPWithStalledHandshake(t *testing.T) {
	stalled, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	accepted := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := stalled.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		io.Copy(io.Discard, conn) // TCP succeeds, TLS never responds.
	}()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	config := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	config.ServerName = "example.com"
	dialer := &tls.Dialer{Config: config}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := raceTLSAddresses(ctx, []string{stalled.Addr().String(), server.Listener.Addr().String()}, 25*time.Millisecond,
		func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.RemoteAddr().String() != server.Listener.Addr().String() {
		t.Fatal("did not choose working TLS endpoint")
	}
	if !conn.(*tls.Conn).ConnectionState().HandshakeComplete || config.InsecureSkipVerify {
		t.Fatal("TLS fallback bypassed certificate verification")
	}
	select {
	case <-accepted:
	default:
		t.Fatal("first TCP connection was not established")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("losing handshake connection was not closed")
	}
}

func TestDirectTransportPreservesTLSHTTP2AndSingleRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.TLS.ServerName != "example.com" || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("lost original hostname or authorization")
		}
		io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	transport := newDirectHTTPTransport()
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	defer transport.CloseIdleConnections()
	h := &http.Client{Transport: transport, Timeout: time.Second}
	req, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	req.Header.Set("Authorization", "Bearer test")
	response, err := h.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || requests.Load() != 1 || transport.Proxy != nil {
		t.Fatalf("protocol/request/proxy changed: %s, requests=%d", response.Proto, requests.Load())
	}
}

func TestDirectTransportRejectsUntrustedCertificate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	transport := newDirectHTTPTransport()
	defer transport.CloseIdleConnections()
	_, err := (&http.Client{Transport: transport, Timeout: time.Second}).Get(server.URL)
	var certificate *tls.CertificateVerificationError
	if !errors.As(err, &certificate) || requests.Load() != 0 {
		t.Fatalf("untrusted endpoint received HTTP or lost certificate error: %v", err)
	}
}

func TestTLSRaceCancellationAndLateSuccessCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started, closed := make(chan struct{}), make(chan struct{})
	peer, late := net.Pipe()
	defer peer.Close()
	result := make(chan error, 1)
	go func() {
		_, err := raceTLSAddresses(ctx, []string{"test"}, time.Second, func(ctx context.Context, _ string) (net.Conn, error) {
			close(started)
			<-ctx.Done()
			return late, nil
		})
		result <- err
	}()
	go func() { _, _ = peer.Read(make([]byte, 1)); close(closed) }()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for fallback")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late successful connection leaked")
	}
}

func TestTLSRaceImmediateFailureAndSingleFamilyAddresses(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := raceTLSAddresses(ctx, []string{"one", "two"}, time.Hour, func(_ context.Context, address string) (net.Conn, error) {
		if address == "one" {
			return nil, first
		}
		return nil, second
	})
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("fast failure waited for timer or dropped error: %v", err)
	}
	for _, addresses := range [][]string{{"::1", "::2"}, {"127.0.0.1", "127.0.0.2"}, {"::1", "::2", "127.0.0.1", "127.0.0.1"}} {
		var ips []net.IPAddr
		for _, ip := range addresses {
			ips = append(ips, net.IPAddr{IP: net.ParseIP(ip)})
		}
		got := interleaveAddresses(ips, "443")
		want := []string{net.JoinHostPort(addresses[0], "443"), net.JoinHostPort(addresses[1], "443")}
		if len(addresses) > 2 {
			want = []string{"[::1]:443", "127.0.0.1:443", "[::2]:443"}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("address families lost or not interleaved: %v", got)
		}
	}
}

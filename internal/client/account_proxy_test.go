package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewProxySelectionHonorsExplicitSettings(t *testing.T) {
	for _, tc := range []struct {
		server, bypass, host, want string
	}{
		{"127.0.0.1:7897", "localhost;127.*;<local>", "hqip.sharevps.online", "127.0.0.1:7897"},
		{"http=127.0.0.1:7890;https=localhost:7897", "", "example.com", "127.0.0.1:7897"},
		{"http://[::1]:7897", "", "example.com", "[::1]:7897"},
		{"127.0.0.1:7897", "*.sharevps.online", "hqip.sharevps.online", ""},
		{"127.0.0.1:7897", "<local>;hqip.sharevps.online", "hqip.sharevps.online", ""},
		{"127.0.0.1:7897", "<local>", "intranet", ""},
		{"127.0.0.1:7897", "", "127.0.0.1", ""},
		{"127.0.0.1:7897", "", "localhost", ""},
		{"http=127.0.0.1:7890;socks=127.0.0.1:7891", "", "example.com", ""},
		{"proxy.example:7897", "", "example.com", ""},
		{"192.168.1.1:7897", "", "example.com", ""},
		{"http://user:pass@127.0.0.1:7897", "", "example.com", ""},
		{"https://127.0.0.1:7897", "", "example.com", ""},
		{"127.0.0.1:7897/path", "", "example.com", ""},
		{"127.0.0.1:0", "", "example.com", ""},
		{"127.0.0.1:99999", "", "example.com", ""},
	} {
		if got := previewLoopbackProxy(tc.server, tc.bypass, tc.host); got != tc.want {
			t.Errorf("proxy %q bypass %q for %q = %q, want %q", tc.server, tc.bypass, tc.host, got, tc.want)
		}
	}
}

func proxyTLSTestServer(t *testing.T, upstream string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	connects := &atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connects.Add(1)
		if r.Method != http.MethodConnect || r.Host != "example.com:443" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" || r.ContentLength > 0 {
			t.Error("proxy received account path, credentials or request body")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		remote, err := net.Dial("tcp", upstream)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			remote.Close()
			return
		}
		defer conn.Close()
		defer remote.Close()
		io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		done := make(chan struct{})
		go func() { io.Copy(remote, conn); remote.Close(); close(done) }()
		io.Copy(conn, remote)
		conn.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	return proxy, connects
}

func TestAccountProxyFallbackKeepsTLSAndSendsPostOnce(t *testing.T) {
	var posts atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.TLS.ServerName != "example.com" || r.Header.Get("Authorization") != "Bearer test-account" || string(body) != "one-time-code" {
			t.Error("end-to-end TLS hostname, authorization or body lost")
		}
		io.WriteString(w, "ok")
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	proxy, connects := proxyTLSTestServer(t, upstream.Listener.Addr().String())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialTLSContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialAccountTLS(ctx, func(ctx context.Context) (net.Conn, error) {
			// Emulate a physical socket waiting on a TUN-only Fake-IP answer.
			<-ctx.Done()
			return nil, ctx.Err()
		}, func(ctx context.Context) (net.Conn, error) {
			return dialAccountProxyTLS(ctx, proxy.Listener.Addr().String(), address, transport.TLSClientConfig.Clone())
		}, 15*time.Millisecond)
	}
	defer transport.CloseIdleConnections()
	h := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/api/v3/client/token", strings.NewReader("one-time-code"))
	req.Header.Set("Authorization", "Bearer test-account")
	response, err := h.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ProtoMajor != 2 || posts.Load() != 1 || connects.Load() != 1 || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("TLS/fallback request invariant failed: status=%d protocol=%s posts=%d connects=%d", response.StatusCode, response.Proto, posts.Load(), connects.Load())
	}
}

func TestAccountProxyDoesNotRelaxCertificateVerification(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	upstream.Config.ErrorLog = log.New(io.Discard, "", 0)
	upstream.StartTLS()
	defer upstream.Close()
	proxy, _ := proxyTLSTestServer(t, upstream.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := dialAccountProxyTLS(ctx, proxy.Listener.Addr().String(), "example.com:443", &tls.Config{ServerName: "example.com"})
	var certificate *tls.CertificateVerificationError
	if !errors.As(err, &certificate) || requests.Load() != 0 {
		t.Fatalf("untrusted TLS accepted or credentials sent: %v requests=%d", err, requests.Load())
	}
}

func TestAccountTLSFallbackSkipsSuccessfulDirectCertificateFailureAndCancellation(t *testing.T) {
	for _, name := range []string{"success", "certificate", "canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls int
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			conn, err := dialAccountTLS(ctx, func(context.Context) (net.Conn, error) {
				switch name {
				case "success":
					return left, nil
				case "certificate":
					return nil, &tls.CertificateVerificationError{Err: errors.New("untrusted")}
				default:
					cancel()
					return nil, context.Canceled
				}
			}, func(context.Context) (net.Conn, error) { calls++; return nil, errors.New("must not run") }, time.Second)
			if calls != 0 || name == "success" && (conn != left || err != nil) || name != "success" && err == nil {
				t.Fatalf("unexpected fallback: calls=%d conn=%v err=%v", calls, conn, err)
			}
		})
	}
}

func TestAccountProxyCancellationClosesStalledConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		http.ReadRequest(bufio.NewReader(conn))
		io.Copy(io.Discard, conn)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = dialAccountProxyTLS(ctx, listener.Addr().String(), "example.com:443", &tls.Config{ServerName: "example.com"})
	if err == nil {
		t.Fatal("stalled CONNECT unexpectedly succeeded")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation left the proxy socket open")
	}
}

package reality_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/reality"
	hy "github.com/apernet/hysteria/core/v2/client"
	hs "github.com/apernet/hysteria/core/v2/server"
	utls "github.com/metacubex/utls"
)

type rejectLegacy struct{}

func (rejectLegacy) Authenticate(net.Addr, string, uint64) (bool, string) { return false, "" }

// This opt-in wire test uses the released desktop core, not a second copy of our
// encoder. Everything else is loopback: Reality target, guarded HY2 backend,
// admission and TCP/UDP destinations. No production credentials are required.
func TestSingBoxRealityGuardedWire(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX_EXE")
	if binary == "" {
		t.Skip("set HQIP_SINGBOX_EXE to the verified desktop sing-box executable")
	}
	testRealityGuardedWire(t, binary, false)
}

func TestXrayRealityGuardedWire(t *testing.T) {
	binary := os.Getenv("HQIP_XRAY_EXE")
	if binary == "" {
		t.Skip("set HQIP_XRAY_EXE to an installed or verified Xray executable")
	}
	testRealityGuardedWire(t, binary, true)
}

func testRealityGuardedWire(t *testing.T, binary string, xray bool) {
	if !reality.VisionEnabled {
		t.Fatal("wire regression requires the release build tag: -tags=with_utls")
	}
	for _, flow := range []string{"", model.RealityVisionFlow} {
		name := flow
		if name == "" {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			password := strings.Repeat("a", 43)
			uuid := model.RealityUUID(password)
			var opens, resolutions atomic.Int32
			var denied atomic.Bool
			var mu sync.Mutex
			var reported int64
			admissionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
					http.Error(w, "secret", 403)
					return
				}
				if denied.Load() {
					http.Error(w, "revoked", 403)
					return
				}
				if r.URL.Path == "/admission" {
					var request map[string]string
					_ = json.NewDecoder(r.Body).Decode(&request)
					if request["password"] != password {
						http.Error(w, "credential", 403)
						return
					}
					opens.Add(1)
				} else {
					var usage struct{ UploadBytes, DownloadBytes int64 }
					_ = json.NewDecoder(r.Body).Decode(&usage)
					mu.Lock()
					reported = max(reported, usage.UploadBytes+usage.DownloadBytes)
					mu.Unlock()
				}
				_ = json.NewEncoder(w).Encode(hs.LeaseGrant{LeaseID: "wire-lease", UserID: "wire-user", AccountingID: "wire-scope", ExpiresAt: time.Now().Add(120 * time.Second), BudgetBytes: 16 << 20, Revision: 1, RateVersion: 1})
			}))
			defer admissionServer.Close()
			admission, err := hs.NewAdmission(hs.AdmissionConfig{URL: admissionServer.URL, Secret: strings.Repeat("s", 32)})
			if err != nil {
				t.Fatal(err)
			}
			targetTLS := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			targetTLS.EnableHTTP2 = true
			targetTLS.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
			targetTLS.StartTLS()
			defer targetTLS.Close()
			pool := x509.NewCertPool()
			pool.AddCert(targetTLS.Certificate())
			packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			backend, err := hs.NewServer(&hs.Config{Conn: packet, TLSConfig: hs.TLSConfig{Certificates: targetTLS.TLS.Certificates}, Authenticator: rejectLegacy{}, HQIPAdmission: admission})
			if err != nil {
				t.Fatal(err)
			}
			go backend.Serve()
			defer backend.Close()
			private, _ := ecdh.X25519().GenerateKey(rand.Reader)
			short := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			endpoint := model.RealityEndpoint{Port: listener.Addr().(*net.TCPAddr).Port, ServerName: "example.com", PublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), ShortID: hex.EncodeToString(short[:]), Flow: flow}
			gateway, err := reality.New(reality.Config{
				TLS: &utls.RealityConfig{Config: utls.Config{ServerName: "example.com", SessionTicketsDisabled: true}, Type: "tcp", Dest: targetTLS.Listener.Addr().String(), DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, ServerNames: map[string]bool{"example.com": true}, PrivateKey: private.Bytes(), ShortIds: map[[8]byte]bool{short: true}, MaxTimeDiff: 2 * time.Minute},
				Resolve: func(_ context.Context, id string) (string, error) {
					resolutions.Add(1)
					if id != uuid {
						return "", fmt.Errorf("unknown alias")
					}
					return password, nil
				},
				Connect: func(_ context.Context, credential, clientIP string) (hy.Client, error) {
					c, _, e := hy.NewClient(&hy.Config{ServerAddr: packet.LocalAddr(), Auth: credential, TLSConfig: hy.TLSConfig{ServerName: "example.com", RootCAs: pool}, QUICConfig: hy.QUICConfig{DisableChromeParrot: true}})
					return c, e
				},
				Idle: 200 * time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			gateCtx, stopGate := context.WithCancel(ctx)
			gateDone := make(chan error, 1)
			go func() { gateDone <- gateway.Serve(gateCtx, listener) }()
			defer func() {
				stopGate()
				if e := <-gateDone; e != nil {
					t.Error(e)
				}
			}()
			probe, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = reality.Handshake(ctx, probe, endpoint); err != nil {
				probe.Close()
				t.Fatalf("authenticated latency handshake: %v", err)
			}
			probe.Close()
			if opens.Load() != 0 || resolutions.Load() != 0 {
				t.Fatal("latency check acquired a business lease")
			}
			// Exercise a DNS dial address distinct from the Reality camouflage SNI.
			node := model.ClientNode{Protocol: "vless", Server: "localhost", Port: endpoint.Port, SNI: endpoint.ServerName, Password: uuid, Reality: &endpoint}
			outbound, err := node.SingBoxOutbound("proxy")
			if err != nil {
				t.Fatal(err)
			}
			// Reserve only long enough to choose a loopback port; child readiness is
			// checked separately, and an unexpected bind failure fails the test.
			reserve, _ := net.Listen("tcp4", "127.0.0.1:0")
			mixedPort := reserve.Addr().(*net.TCPAddr).Port
			reserve.Close()
			// Resolve the test-only hostname inside the child instead of relying on a
			// router to answer localhost DNS queries. Production resolves allocated DNS.
			configuration := map[string]any{"log": map[string]any{"level": "trace"}, "dns": map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "local", "predefined": map[string]any{"localhost": "127.0.0.1"}}}, "strategy": "prefer_ipv4"}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": mixedPort}}, "outbounds": []any{outbound}, "route": map[string]any{"final": "proxy", "default_domain_resolver": "local"}}
			udpPort := mixedPort
			if xray {
				reserve, e := net.Listen("tcp4", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				udpPort = reserve.Addr().(*net.TCPAddr).Port
				reserve.Close()
				configuration = map[string]any{"log": map[string]any{"loglevel": "debug"}, "inbounds": []any{
					map[string]any{"listen": "127.0.0.1", "port": mixedPort, "protocol": "http"},
					map[string]any{"listen": "127.0.0.1", "port": udpPort, "protocol": "socks", "settings": map[string]any{"udp": true, "ip": "127.0.0.1"}},
				}, "outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": node.Server, "port": node.Port, "users": []any{map[string]any{"id": uuid, "encryption": "none", "flow": flow}}}}}, "streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": endpoint.ServerName, "fingerprint": "chrome", "publicKey": endpoint.PublicKey, "shortId": endpoint.ShortID}}}}}
			}
			data, _ := json.Marshal(configuration)
			folder := t.TempDir()
			configPath := filepath.Join(folder, "client.json")
			if err = os.WriteFile(configPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			command := exec.CommandContext(ctx, binary, "run", "-c", configPath)
			command.Stdout = &output
			command.Stderr = &output
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = command.Process.Kill()
				_ = command.Wait()
				if flow != "" && !xray && !strings.Contains(output.String(), "XtlsRead readV") {
					t.Error("TLS 1.3 traffic did not exercise Vision direct read")
				}
				if t.Failed() {
					t.Log(output.String())
				}
			}()
			proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort))
			for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				c, e := net.DialTimeout("tcp", proxyAddress, 50*time.Millisecond)
				if e == nil {
					c.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("sing-box did not listen")
				}
			}
			proxyURL, _ := url.Parse("http://" + proxyAddress)
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
			payload := bytes.Repeat([]byte("guarded-reality-payload-"), 4096)
			echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
			defer echo.Close()
			for i := 0; i < 3; i++ {
				response, e := client.Get(echo.URL)
				if e != nil {
					t.Fatal(e)
				}
				body, e := io.ReadAll(response.Body)
				response.Body.Close()
				if e != nil || !bytes.Equal(body, payload) {
					t.Fatalf("TCP payload mismatch: bytes=%d err=%v", len(body), e)
				}
			}
			// Exercise both Vision branches: TLS 1.2 keeps outer encryption; TLS 1.3
			// permits raw forwarding after padded negotiation. Large uploads and
			// downloads span TLS records, checking buffered data at the transition.
			for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
				secureEcho := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, e := io.ReadAll(r.Body)
					if e != nil || !bytes.Equal(body, payload) {
						http.Error(w, "upload mismatch", 400)
						return
					}
					_, _ = w.Write(body)
				}))
				secureEcho.TLS = &tls.Config{MinVersion: version, MaxVersion: version}
				secureEcho.StartTLS()
				defer secureEcho.Close()
				securePool := x509.NewCertPool()
				securePool.AddCert(secureEcho.Certificate())
				secureTransport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: securePool, MinVersion: version, MaxVersion: version}}
				defer secureTransport.CloseIdleConnections()
				secureClient := &http.Client{Transport: secureTransport, Timeout: 8 * time.Second}
				for i := 0; i < 3; i++ {
					response, e := secureClient.Post(secureEcho.URL, "application/octet-stream", bytes.NewReader(payload))
					if e != nil {
						t.Fatalf("TLS %x: %v", version, e)
					}
					body, e := io.ReadAll(response.Body)
					response.Body.Close()
					if e != nil || response.TLS.Version != version || !bytes.Equal(body, payload) {
						t.Fatalf("TLS %x payload mismatch: bytes=%d err=%v", version, len(body), e)
					}
				}
			}
			if opens.Load() != 1 || resolutions.Load() != 1 {
				t.Fatalf("streams did not share one guarded lease: opens=%d resolves=%d", opens.Load(), resolutions.Load())
			}
			testSOCKSUDP(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(udpPort)))
			// A closed backend must report traffic and release its slot; subsequent
			// admission denial must prevent another forwarded response.
			time.Sleep(500 * time.Millisecond)
			mu.Lock()
			usage := reported
			mu.Unlock()
			if usage < int64(len(payload)*15) {
				t.Fatalf("payload escaped accounting: %d", usage)
			}
			denied.Store(true)
			if response, e := client.Get(echo.URL); e == nil {
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode == 200 || bytes.Equal(body, payload) {
					t.Fatal("revoked admission forwarded traffic")
				}
			}
		})
	}
}

func testSOCKSUDP(t *testing.T, proxyAddress string) {
	t.Helper()
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		data := make([]byte, 2048)
		n, addr, e := echo.ReadFrom(data)
		if e == nil {
			_, _ = echo.WriteTo(data[:n], addr)
		}
	}()
	control, err := net.DialTimeout("tcp", proxyAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = control.Write([]byte{5, 1, 0})
	var method [2]byte
	if _, err = io.ReadFull(control, method[:]); err != nil || method != [2]byte{5, 0} {
		t.Fatalf("SOCKS method: %v %v", method, err)
	}
	_, _ = control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	var reply [10]byte
	if _, err = io.ReadFull(control, reply[:]); err != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatalf("SOCKS UDP associate: %v %v", reply, err)
	}
	relay := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:]))}
	if relay.IP.IsUnspecified() {
		relay.IP = net.ParseIP("127.0.0.1")
	}
	socket, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	_ = socket.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("guarded-xudp-roundtrip")
	packet := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 0}, payload...)
	binary.BigEndian.PutUint16(packet[8:10], uint16(echo.LocalAddr().(*net.UDPAddr).Port))
	if _, err = socket.Write(packet); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2048)
	n, err := socket.Read(response)
	if err != nil || n < 10 || !bytes.Equal(response[10:n], payload) {
		t.Fatalf("UDP payload mismatch: %d %v", n, err)
	}
}

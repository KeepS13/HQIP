package client

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Runs the pinned binary against an isolated local HY2 server and lossy UDP
// relay. No production credentials, system proxy changes or public nodes.
func TestPinnedCoreRecoveryIntegration(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX_EXE")
	if binary == "" {
		t.Skip("set HQIP_SINGBOX_EXE to run isolated pinned-core acceptance")
	}
	core := &Core{Path: binary, Dir: t.TempDir(), Platform: fakePlatform{}}
	tun := os.Getenv("HQIP_RECOVERY_TUN") == "1"
	if err := core.verifyBinary(); err != nil {
		t.Fatal(err)
	}
	cert, key, pool := recoveryCertificate(t, core.Dir)
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.2")})
	if err != nil {
		t.Fatal(err)
	}
	serverPort := listener.LocalAddr().(*net.UDPAddr).Port
	listener.Close()
	server := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "hysteria2", "listen": "127.0.0.2", "listen_port": serverPort, "users": []any{map[string]any{"password": "isolated-recovery-test-password"}}, "tls": map[string]any{"enabled": true, "certificate_path": cert, "key_path": key}}}, "outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}, "route": map[string]any{"final": "direct"}}
	server["route"].(map[string]any)["rules"] = []any{map[string]any{"ip_cidr": []string{"198.18.0.1/32"}, "action": "route", "outbound": "direct", "override_address": "127.0.0.1"}}
	startAcceptanceCore(t, binary, server, filepath.Join(core.Dir, "server.json"))
	relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.2")})
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.2"), Port: serverPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { relay.Close(); upstream.Close() })
	var drop atomic.Bool
	var peer atomic.Pointer[net.UDPAddr]
	go func() {
		buf := make([]byte, 65536)
		for {
			n, addr, e := relay.ReadFromUDP(buf)
			if e != nil {
				return
			}
			peer.Store(addr)
			if !drop.Load() {
				upstream.Write(buf[:n])
			}
		}
	}()
	go func() {
		buf := make([]byte, 65536)
		for {
			n, e := upstream.Read(buf)
			if e != nil {
				return
			}
			if addr := peer.Load(); addr != nil && !drop.Load() {
				relay.WriteToUDP(buf[:n], addr)
			}
		}
	}()
	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("127.0.0.1")) }))
	t.Cleanup(target.Close)
	pool.AddCert(target.Certificate())
	// Trust only the two local acceptance certificates, never disable TLS.
	oldTransport := http.DefaultTransport
	transport := oldTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.RootCAs = pool
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport; transport.CloseIdleConnections() })
	oldTargets := exitProbeTargets
	exitProbeTargets = []exitProbeTarget{{"isolated", target.URL, false}}
	t.Cleanup(func() { exitProbeTargets = oldTargets })
	core.port, _ = freePort()
	core.probePort, _ = freePort()
	core.apiPort, _ = freePort()
	core.secret = "isolated-acceptance-secret"
	p := Profile{Nodes: []Node{{ID: "local", Server: "127.0.0.2", Port: relay.LocalAddr().(*net.UDPAddr).Port, SNI: "localhost", Password: "isolated-recovery-test-password", Ready: true}}}
	generated, err := GenerateConfig(ConfigOptions{Profile: p, Selected: "local", Mode: "global", TUN: tun, MixedPort: core.port, ProbePort: core.probePort, APIPort: core.apiPort, APISecret: core.secret, ServerURL: "https://control.test"})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	json.Unmarshal(generated, &config)
	if tun {
		// Exercise the native TUN data path with only a benchmark /32 route.
		// Never replace the user's default route, DNS, or installed Helper.
		inbound := config["inbounds"].([]any)[2].(map[string]any)
		inbound["interface_name"] = "HQIPAcceptance"
		inbound["address"] = []string{"172.28.92.1/30"}
		inbound["route_address"] = []string{"198.18.0.1/32"}
		inbound["strict_route"] = false
	}
	config["outbounds"].([]any)[1].(map[string]any)["tls"].(map[string]any)["certificate_path"] = cert
	// Force the local business target through HY2 rather than the ordinary LAN
	// bypass. All pause/probe/control rules remain exactly as generated.
	route := config["route"].(map[string]any)
	var filtered []any
	for _, item := range route["rules"].([]any) {
		if _, private := item.(map[string]any)["ip_is_private"]; !private {
			filtered = append(filtered, item)
		}
	}
	route["rules"] = filtered
	process := startAcceptanceCore(t, binary, config, filepath.Join(core.Dir, "client.json"))
	pid := process.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var status map[string]any
		err = readCoreJSON(ctx, core.apiPort, core.secret, "/configs", &status)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("core management startup failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	verify := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if e := core.VerifyCurrent(ctx, "127.0.0.1"); e != nil {
			t.Fatal(e)
		}
	}
	verify()
	t.Log("initial verified HY2 connection")
	proxyURL, _ := url.Parse("http://127.0.0.1:" + portString(core.port))
	businessTransport := transport.Clone()
	businessTransport.Proxy = http.ProxyURL(proxyURL)
	businessURL := target.URL
	if tun {
		businessTransport.Proxy = nil
		businessTransport.TLSClientConfig.ServerName = "127.0.0.1"
		targetURL, _ := url.Parse(target.URL)
		businessURL = "https://198.18.0.1:" + targetURL.Port()
	}
	business := &http.Client{Transport: businessTransport, Timeout: 3 * time.Second}
	defer businessTransport.CloseIdleConnections()
	request := func() error {
		res, e := business.Get(businessURL)
		if e != nil {
			return e
		}
		defer res.Body.Close()
		_, e = io.ReadAll(res.Body)
		return e
	}
	if err = request(); err != nil {
		t.Fatal(err)
	}
	if err = core.SetSuspended(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	if request() == nil {
		t.Fatal("paused business traffic was accepted")
	}
	if hits.Load() != before {
		t.Fatal("paused business traffic escaped direct")
	}
	verify()
	t.Log("paused business blocked, controlled HTTPS probe preserved")
	if err = core.SetSuspended(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err = request(); err != nil {
		t.Fatal(err)
	}
	durations := []time.Duration{2 * time.Second, 70 * time.Second}
	if testing.Short() {
		durations = durations[:1]
	}
	for _, duration := range durations {
		drop.Store(true)
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = core.VerifyCurrent(ctx, "127.0.0.1")
		cancel()
		if err == nil || !retryable(err) {
			t.Fatalf("UDP loss classification: %v", err)
		}
		if rest := duration - time.Since(start); rest > 0 {
			time.Sleep(rest)
		}
		drop.Store(false)
		verify()
		if process.Process.Pid != pid {
			t.Fatal("core was replaced")
		}
		t.Logf("recovered after %s UDP loss with same PID", duration)
	}
	// Change only SNI in a second isolated process: a HY2 certificate fault must
	// remain terminal even though CONNECT otherwise looks like a network error.
	core2 := &Core{Path: binary, Dir: core.Dir, Platform: fakePlatform{}, secret: core.secret}
	core2.port, _ = freePort()
	core2.probePort, _ = freePort()
	core2.apiPort, _ = freePort()
	config["outbounds"].([]any)[1].(map[string]any)["tls"].(map[string]any)["server_name"] = "wrong.invalid"
	config["inbounds"].([]any)[0].(map[string]any)["listen_port"] = core2.port
	config["inbounds"].([]any)[1].(map[string]any)["listen_port"] = core2.probePort
	config["experimental"].(map[string]any)["clash_api"].(map[string]any)["external_controller"] = "127.0.0.1:" + portString(core2.apiPort)
	startAcceptanceCore(t, binary, config, filepath.Join(core.Dir, "invalid-cert.json"))
	time.Sleep(200 * time.Millisecond)
	err = core2.VerifyCurrent(context.Background(), "127.0.0.1")
	if err == nil || retryable(err) {
		t.Fatalf("HY2 certificate fault was recoverable: %v", err)
	}
	t.Log("HY2 certificate failure stops recovery")
}

func startAcceptanceCore(t *testing.T, binary string, config any, path string) *exec.Cmd {
	t.Helper()
	data, _ := json.Marshal(config)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func recoveryCertificate(t *testing.T, dir string) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "local-cert.pem"), filepath.Join(dir, "local-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	os.WriteFile(certPath, certPEM, 0600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return certPath, keyPath, pool
}

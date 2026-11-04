package integration_tests

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/server"
)

// Set HQIP_SINGBOX to a pinned, official sing-box executable. This test uses an
// isolated mixed inbound; it does not change the system proxy, routes or DNS.
func TestHQIPSingBoxStrictTLSHTTPSLeaseRevocation(t *testing.T) {
	testHQIPWireClient(t, "sing-box", os.Getenv("HQIP_SINGBOX"))
}

func TestHQIPMihomoStrictTLSHTTPSLeaseRevocation(t *testing.T) {
	testHQIPWireClient(t, "mihomo", os.Getenv("HQIP_MIHOMO"))
}

func testHQIPWireClient(t *testing.T, kind, binary string) {
	if binary == "" {
		t.Skip("official client binary not provided")
	}
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hqip-test.invalid"}, DNSNames: []string{"hqip-test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPath := filepath.Join(dir, "ca.pem")
	if err = os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	var revoked atomic.Bool
	var accepted, denied, finalBytes atomic.Int64
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-admission-secret-long-enough" {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/admission" {
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if revoked.Load() || b["password"] != "sing-box-wire-protocol-test" || !accepted.CompareAndSwap(0, 1) {
				denied.Add(1)
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).Encode(server.LeaseGrant{LeaseID: b["connectionId"], AccountingID: "test-device", UserID: "test-user", ExpiresAt: time.Now().Add(6 * time.Second), BudgetBytes: 1 << 20, BandwidthBytesPerSecond: 50000, Revision: 1, RateVersion: 1})
			return
		}
		var usage struct {
			UploadBytes, DownloadBytes int64
			Closed                     bool
		}
		_ = json.NewDecoder(r.Body).Decode(&usage)
		if usage.Closed {
			finalBytes.Add(usage.UploadBytes + usage.DownloadBytes)
		}
		w.WriteHeader(403)
	}))
	defer central.Close()
	admission, err := server.NewAdmission(server.AdmissionConfig{URL: central.URL, Secret: "test-admission-secret-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.NewServer(&server.Config{TLSConfig: server.TLSConfig{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, Conn: pc, Authenticator: forbiddenFallback{}, HQIPAdmission: admission})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()
	port := startHQIPWireClient(t, kind, binary, certPath, string(certPEM), pc.LocalAddr().(*net.UDPAddr).Port)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("HTTPS via strict TLS HY2")) }))
	defer target.Close()
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	makeHTTPClient := func(port int) *http.Client {
		proxyURL, _ := url.Parse("http://" + net.JoinHostPort("127.0.0.1", itoa(port)))
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 3 * time.Second}
	}
	client := makeHTTPClient(port)
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("official %s could not use HQIP HY2: %v", kind, err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "HTTPS via strict TLS HY2" || accepted.Load() != 1 {
		t.Fatal("wrong proxy response or admission count", accepted.Load())
	}
	secondPort := startHQIPWireClient(t, kind, binary, certPath, string(certPEM), pc.LocalAddr().(*net.UDPAddr).Port)
	if response, err = makeHTTPClient(secondPort).Get(target.URL); err == nil {
		response.Body.Close()
		t.Fatal("second real QUIC connection bypassed slot denial")
	}
	if denied.Load() == 0 {
		t.Fatal("second instance did not reach admission")
	}
	response, err = client.Get(target.URL)
	if err != nil {
		t.Fatal("denying second slot interrupted original slot", err)
	}
	response.Body.Close()
	revoked.Store(true)
	time.Sleep(6200 * time.Millisecond)
	if response, err = client.Get(target.URL); err == nil {
		response.Body.Close()
		t.Fatal("expired/revoked connection still forwards")
	}
	for deadline := time.Now().Add(time.Second); finalBytes.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if finalBytes.Load() == 0 {
		t.Fatal("actual proxy payload did not produce final usage")
	}
}

func startHQIPWireClient(t *testing.T, kind, binary, certPath, certPEM string, serverPort int) int {
	t.Helper()
	dir := t.TempDir()
	port := 0
	for attempt := 0; attempt < 100; attempt++ {
		inbound, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		candidate := inbound.Addr().(*net.TCPAddr).Port
		udp, e := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", itoa(candidate)))
		inbound.Close()
		if e == nil {
			udp.Close()
			port = candidate
			break
		}
	}
	if port == 0 {
		t.Fatal("no isolated TCP+UDP client port available")
	}
	config := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}}, "outbounds": []any{map[string]any{"type": "hysteria2", "tag": "node", "server": "127.0.0.1", "server_port": serverPort, "password": "sing-box-wire-protocol-test", "tls": map[string]any{"enabled": true, "server_name": "hqip-test.invalid", "certificate_path": certPath}}}, "route": map[string]any{"final": "node"}}
	controller := ""
	if kind == "mihomo" {
		config = map[string]any{"mixed-port": port, "bind-address": "127.0.0.1", "allow-lan": false, "mode": "rule", "log-level": "error", "geodata-mode": false, "geo-auto-update": false, "dns": map[string]any{"enable": false}, "tls": map[string]any{"custom-certifactes": []string{certPEM}}, "proxies": []any{map[string]any{"name": "node", "type": "hysteria2", "server": "127.0.0.1", "port": serverPort, "password": "sing-box-wire-protocol-test", "sni": "hqip-test.invalid", "skip-cert-verify": false}}, "rules": []string{"MATCH,node"}}
		config["log-level"] = "debug"
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		controller = listener.Addr().String()
		listener.Close()
		config["external-controller"] = controller
		config["secret"] = "isolated-test-controller-secret"
	}
	data, _ := json.Marshal(config)
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	check := exec.Command(binary, "check", "-c", cfgPath)
	if kind == "mihomo" {
		check = exec.Command(binary, "-t", "-d", dir, "-f", cfgPath)
	}
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("%s config: %v %s", kind, err, output)
	}
	var logs bytes.Buffer
	process := exec.Command(binary, "run", "-c", cfgPath)
	if kind == "mihomo" {
		process = exec.Command(binary, "-d", dir, "-f", cfgPath)
	}
	process.Stdout = &logs
	process.Stderr = &logs
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
		if t.Failed() {
			t.Log(kind, logs.String())
		}
	})
	for deadline := time.Now().Add(5 * time.Second); ; {
		c, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 100*time.Millisecond)
		if e == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			_ = process.Process.Kill()
			_ = process.Wait()
			t.Fatalf("%s inbound not ready: %s", kind, logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if kind == "mihomo" {
		// Mihomo parses initial proxy TLS pools before installing custom roots.
		// Reparse this isolated config once after root installation; normal public
		// CA certificates do not need this test-fixture-only preparation.
		payload, _ := json.Marshal(map[string]string{"path": cfgPath})
		request, _ := http.NewRequest(http.MethodPut, "http://"+controller+"/configs", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer isolated-test-controller-secret")
		request.Header.Set("Content-Type", "application/json")
		localClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		response, e := localClient.Do(request)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		localClient.CloseIdleConnections()
		if response.StatusCode != http.StatusNoContent {
			t.Fatal("Mihomo isolated CA setup", response.StatusCode)
		}
	}
	if strings.Contains(string(data), `"insecure":true`) || strings.Contains(string(data), `"skip-cert-verify":true`) {
		t.Fatal("test accidentally disabled node TLS verification")
	}
	return port
}

func itoa(v int) string { return new(big.Int).SetInt64(int64(v)).String() }

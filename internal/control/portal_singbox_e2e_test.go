package control

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

// Explicit opt-in. Every address and process belongs to this test; the exported
// TUN is checked but never started, and no system proxy or DNS setting is changed.
func TestPortalSingBoxExportRealHY2Routing(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX")
	if binary == "" {
		t.Skip("HQIP_SINGBOX is required for isolated real-client verification")
	}
	var err error
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	certificate, certPEM, keyPEM := portalSingBoxTestCertificate(t)
	certPath, keyPath := filepath.Join(root, "ca.pem"), filepath.Join(root, "key.pem")
	for path, body := range map[string][]byte{certPath: certPEM, keyPath: keyPEM} {
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var proxyHits, directHits, rejectHits atomic.Int32
	proxyTarget := portalSingBoxTestTarget(t, "127.0.0.21", certificate, &proxyHits)
	directTarget := portalSingBoxTestTarget(t, "127.0.0.22", certificate, &directHits)
	rejectTarget := portalSingBoxTestTarget(t, "127.0.0.23", certificate, &rejectHits)
	serverPort := portalSingBoxTestPort(t, "udp", "127.0.0.10")
	s, _, _, node, link := portalExportFixture(t)
	if _, err = s.Store.DB.Exec(`UPDATE nodes SET config=json_set(config,'$.publicIp','127.0.0.10','$.domain','portal-export.invalid','$.port',?) WHERE id=?`, serverPort, node.ID); err != nil {
		t.Fatal(err)
	}
	bundle := rules.Bundle{SchemaVersion: rules.SchemaVersion, Source: rules.Source{Kind: "test", Name: "isolated export routes"}, Rules: []rules.Rule{
		{Type: "IP-CIDR", Value: "127.0.0.21/32", Outbound: "proxy", NoResolve: true},
		{Type: "IP-CIDR", Value: "127.0.0.22/32", Outbound: "direct", NoResolve: true},
		{Type: "IP-CIDR", Value: "127.0.0.23/32", Outbound: "reject", NoResolve: true},
		{Type: "MATCH", Outbound: "reject"},
	}}
	if _, err = s.Store.SaveRules(bundle); err != nil {
		t.Fatal(err)
	}
	portal := httptest.NewServer(s.PublicHandler())
	t.Cleanup(portal.Close)
	subscription, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(portal.URL + subscription.EscapedPath() + "?format=singbox")
	if err != nil {
		t.Fatal("fetch exported profile:", err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("profile export returned %d: %v", response.StatusCode, err)
	}
	originalPath := filepath.Join(root, "exported.json")
	if err = os.WriteFile(originalPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, checkErr := exec.CommandContext(ctx, binary, "check", "-c", originalPath).CombinedOutput(); checkErr != nil {
		t.Fatalf("unmodified exported profile failed sing-box check: %v\n%s", checkErr, output)
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	var password string
	for _, value := range config["outbounds"].([]any) {
		outbound := value.(map[string]any)
		if outbound["type"] != "hysteria2" {
			continue
		}
		if password != "" || outbound["server"] != "portal-export.invalid" || outbound["server_port"] != float64(serverPort) {
			t.Fatal("export did not retain the isolated subscription node")
		}
		password, _ = outbound["password"].(string)
		// Supply local endpoint resolution and trust without altering the exported
		// credential, SNI, selector or business routing rules.
		outbound["domain_resolver"] = "fixture"
		outbound["tls"].(map[string]any)["certificate_path"] = certPath
	}
	dns := config["dns"].(map[string]any)
	dns["servers"] = append(dns["servers"].([]any), map[string]any{"type": "hosts", "tag": "fixture", "predefined": map[string]any{"portal-export.invalid": []string{"127.0.0.10"}}})
	if password == "" {
		t.Fatal("export has no subscription credential")
	}
	serverConfig := map[string]any{
		"log": map[string]any{"level": "error"},
		"inbounds": []any{map[string]any{
			"type": "hysteria2", "listen": "127.0.0.10", "listen_port": serverPort,
			"users": []any{map[string]any{"name": "exported-subscription", "password": password}},
			"tls":   map[string]any{"enabled": true, "certificate_path": certPath, "key_path": keyPath},
		}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": "direct"},
	}
	stopServer := portalSingBoxTestProcess(t, binary, root, "server", serverConfig)
	proxyPort := portalSingBoxTestPort(t, "tcp", "127.0.0.1")
	// Preserve the generated selector, node credentials, DNS and ordered routes.
	// Replace only listener plumbing so this test never creates a system TUN.
	config["inbounds"] = []any{map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": proxyPort, "set_system_proxy": false}}
	portalSingBoxTestProcess(t, binary, root, "client", config)
	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		conn, dialErr := net.DialTimeout("tcp", proxyAddress, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated sing-box mixed inbound did not start")
	}
	ca := x509.NewCertPool()
	if !ca.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid test CA")
	}
	proxyURL, _ := url.Parse("http://" + proxyAddress)
	proxyTransport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: ca, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	t.Cleanup(proxyTransport.CloseIdleConnections)
	client := &http.Client{Transport: proxyTransport, Timeout: 5 * time.Second}
	fetch := func(address string, wantSuccess bool) {
		t.Helper()
		res, fetchErr := client.Get(address)
		if fetchErr != nil {
			if wantSuccess {
				t.Fatal("isolated HTTPS request failed:", fetchErr)
			}
			return
		}
		defer res.Body.Close()
		body, readErr := io.ReadAll(res.Body)
		if !wantSuccess || readErr != nil || res.StatusCode != http.StatusOK || string(body) != "exported-route-ok" {
			t.Fatalf("unexpected HTTPS routing result: status=%d readError=%v expectedSuccess=%t", res.StatusCode, readErr, wantSuccess)
		}
	}
	fetch(proxyTarget, true)
	fetch(directTarget, true)
	fetch(rejectTarget, false)
	if proxyHits.Load() != 1 || directHits.Load() != 1 || rejectHits.Load() != 0 {
		t.Fatal("proxy/direct/reject rules did not reach the expected local targets")
	}
	stopServer()
	client.Timeout = 2 * time.Second
	fetch(directTarget, true)
	fetch(proxyTarget, false)
	if proxyHits.Load() != 1 || directHits.Load() != 2 || rejectHits.Load() != 0 {
		t.Fatal("proxy route bypassed the stopped Hysteria server or direct route used it")
	}
	t.Log("HTTP subscription export passed sing-box check, real HY2 HTTPS, direct and reject routing; stopped upstream confirms no proxy bypass")
}

func portalSingBoxTestCertificate(t *testing.T) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(73), Subject: pkix.Name{CommonName: "portal-export.invalid"}, DNSNames: []string{"portal-export.invalid"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.21"), net.ParseIP("127.0.0.22"), net.ParseIP("127.0.0.23")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair, certPEM, keyPEM
}

func portalSingBoxTestTarget(t *testing.T, ip string, certificate tls.Certificate, hits *atomic.Int32) string {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "exported-route-ok")
	}))
	server.Listener.Close()
	server.Listener = listener
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.URL
}

func portalSingBoxTestPort(t *testing.T, network, host string) int {
	t.Helper()
	if network == "udp" {
		listener, err := net.ListenPacket(network, net.JoinHostPort(host, "0"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.LocalAddr().(*net.UDPAddr).Port
	}
	listener, err := net.Listen(network, net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func portalSingBoxTestProcess(t *testing.T, binary, dir, label string, config map[string]any) func() {
	t.Helper()
	path := filepath.Join(dir, label+".json")
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	process := exec.Command(binary, "run", "-c", path)
	process.Dir = dir
	process.Stdout, process.Stderr = &logs, &logs
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = process.Process.Kill()
			_ = process.Wait()
			if t.Failed() {
				t.Log(fmt.Sprintf("%s sing-box log:\n%s", label, logs.String()))
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

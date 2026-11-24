package client

import (
	"bufio"
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
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

func TestRuleResolutionUsesDNSPolicy(t *testing.T) {
	config := dnsTestConfig(t)
	for _, value := range config["route"].(map[string]any)["rules"].([]any) {
		rule := value.(map[string]any)
		if rule["action"] == "resolve" && rule["server"] == "local" {
			t.Fatal("unmatched overseas domains are forced through potentially polluted local DNS")
		}
	}
	servers := config["dns"].(map[string]any)["servers"].([]any)
	if servers[1].(map[string]any)["detour"] != "proxy" {
		t.Fatal("remote DNS must travel through the authenticated node")
	}
}

func dnsTestConfig(t *testing.T) map[string]any {
	t.Helper()
	data, err := GenerateConfig(ConfigOptions{Profile: Profile{Nodes: []Node{{ID: "n", Server: "node.test", SNI: "node.test", Port: 443, Password: "fixture", Ready: true}}}, Bundle: rules.Default(), Selected: "n", Mode: "rule", MixedPort: 14001, APIPort: 14002, ServerURL: DefaultServerURL})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

// The shipped core runs unchanged routing/DNS policy against deterministic
// hosts resolvers and two loopback outbound markers. The local resolver poisons
// one overseas domain to a CN address and has no answer for another. Replacing
// transports/endpoints avoids public DNS, production nodes, TUN and OS proxy.
func TestPinnedCoreRuleDNSPollution(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX_EXE")
	if binary == "" {
		t.Skip("set HQIP_SINGBOX_EXE for isolated routing acceptance")
	}
	core := &Core{Path: binary}
	if err := core.verifyBinary(); err != nil {
		t.Fatal(err)
	}
	proxy := routeMarker(t, "proxy")
	direct := routeMarker(t, "direct")
	config := dnsTestConfig(t)
	dns := config["dns"].(map[string]any)
	dns["servers"] = []any{
		map[string]any{"type": "hosts", "tag": "local", "predefined": map[string]any{"poisoned-overseas.test": []string{"36.0.0.1"}, "bilibili.com": []string{"36.0.0.1"}, "node.test": []string{"127.0.0.1"}}},
		map[string]any{"type": "hosts", "tag": "remote", "predefined": map[string]any{"poisoned-overseas.test": []string{"203.0.113.9"}, "blocked-overseas.test": []string{"203.0.113.10"}, "unlisted-mainland.test": []string{"36.0.0.1"}}},
	}
	config["outbounds"] = []any{direct, proxy}
	listen, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listen.Addr().String()
	port := listen.Addr().(*net.TCPAddr).Port
	listen.Close()
	config["inbounds"] = []any{map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": port, "set_system_proxy": false}}
	delete(config, "experimental")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(config)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "check", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture invalid: %v %s", err, output)
	}
	startAcceptanceCore(t, binary, config, path)
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated core did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	proxyURL, _ := url.Parse("http://" + address)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, test := range []struct{ host, want string }{
		{"poisoned-overseas.test", "proxy"}, {"blocked-overseas.test", "proxy"},
		{"bilibili.com", "direct"}, {"unlisted-mainland.test", "direct"},
	} {
		t.Run(test.host, func(t *testing.T) {
			resp, err := httpClient.Get("http://" + test.host + "/routing-fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != test.want {
				t.Fatalf("route = %q, want %s", body, test.want)
			}
		})
	}
}

// HTTP CONNECT marker reports which outbound actually carried the request.
func routeMarker(t *testing.T, tag string) map[string]any {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, stream, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = stream.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = stream.Flush()
		request, err := http.ReadRequest(bufio.NewReader(stream))
		if err != nil {
			return
		}
		request.Body.Close()
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(tag), tag)
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	return map[string]any{"type": "http", "tag": tag, "server": "127.0.0.1", "server_port": port}
}

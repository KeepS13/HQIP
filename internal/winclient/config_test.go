package winclient

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/rules"
)

func sampleTUN(t *testing.T) map[string]any {
	t.Helper()
	var c map[string]any
	if e := json.Unmarshal([]byte(`{
"log":{"level":"error","timestamp":true},
"dns":{"servers":[{"type":"local","tag":"local"},{"type":"https","tag":"remote","server":"1.1.1.1","server_port":443,"path":"/dns-query","detour":"proxy"}],"rules":[{"domain":["node.example.com"],"server":"local"}],"final":"remote","strategy":"prefer_ipv4"},
"inbounds":[{"type":"mixed","tag":"mixed-in","listen":"127.0.0.1","listen_port":14001,"set_system_proxy":false},{"type":"tun","tag":"tun-in","interface_name":"HQIP","address":["172.28.91.1/30","fdfe:dcba:9876::1/126"],"auto_route":true,"strict_route":true,"stack":"mixed"}],
"outbounds":[{"type":"direct","tag":"direct"},{"type":"hysteria2","tag":"node-1","server":"node.example.com","server_port":443,"password":"random-password-long-enough","tls":{"enabled":true,"server_name":"node.example.com","insecure":false}},{"type":"selector","tag":"proxy","outbounds":["node-1"],"default":"node-1","interrupt_exist_connections":true}],
"route":{"auto_detect_interface":true,"default_domain_resolver":"local","rules":[{"action":"sniff"},{"protocol":"dns","action":"hijack-dns"},{"ip_is_private":true,"action":"route","outbound":"direct"},{"action":"route","outbound":"proxy"}],"final":"proxy","rule_set":[{"type":"local","tag":"geoip-cn","format":"binary","path":"untrusted/file.srs"}]},
"experimental":{"clash_api":{"external_controller":"127.0.0.1:14002","secret":"01234567890123456789012345678901"}}
}`), &c); e != nil {
		t.Fatal(e)
	}
	return c
}

func TestGeneratedClientTUNWithProbeAndNoGeoRules(t *testing.T) {
	profile := client.Profile{Nodes: []client.Node{{ID: "1", Server: "node.example.com", SNI: "node.example.com", Port: 443, Password: "random-password-long-enough", Ready: true}}}
	for _, mode := range []string{"rule", "global"} {
		config, err := client.GenerateConfig(client.ConfigOptions{Profile: profile, Bundle: rules.Default(), Selected: "1", Mode: mode, TUN: true, MixedPort: 14001, ProbePort: 14003, APIPort: 14002, APISecret: "01234567890123456789012345678901", ServerURL: "https://example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = validateTUN(config, "unused.srs"); err != nil {
			t.Fatalf("%s config rejected: %v", mode, err)
		}
	}
}
func TestPrivilegedConfigRewritesClientPath(t *testing.T) {
	c := sampleTUN(t)
	b, _ := json.Marshal(c)
	target := filepath.Join(t.TempDir(), "geoip-cn.srs")
	safe, e := validateTUN(b, target)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(safe), "untrusted") || !strings.Contains(string(safe), filepath.ToSlash(target)) {
		t.Fatal("client file path reached privileged core")
	}
}
func TestPrivilegedConfigRejectsArbitraryFilesListenersAndTLSBypass(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"log-file": func(c map[string]any) { c["log"].(map[string]any)["output"] = "C:/Windows/system.ini" },
		"tls-key": func(c map[string]any) {
			c["outbounds"].([]any)[1].(map[string]any)["tls"].(map[string]any)["key_path"] = "C:/private.key"
		},
		"tls-bypass": func(c map[string]any) {
			c["outbounds"].([]any)[1].(map[string]any)["tls"].(map[string]any)["insecure"] = true
		},
		"public-listener":    func(c map[string]any) { c["inbounds"].([]any)[0].(map[string]any)["listen"] = "0.0.0.0" },
		"proxy-global-write": func(c map[string]any) { c["inbounds"].([]any)[0].(map[string]any)["set_system_proxy"] = true },
		"extra-interface":    func(c map[string]any) { c["inbounds"].([]any)[1].(map[string]any)["interface_name"] = "Ethernet" },
		"cache-file": func(c map[string]any) {
			c["experimental"].(map[string]any)["cache_file"] = map[string]any{"enabled": true, "path": "C:/bad"}
		},
		"remote-rule": func(c map[string]any) {
			c["route"].(map[string]any)["rule_set"].([]any)[0].(map[string]any)["type"] = "remote"
		},
		"unknown-command": func(c map[string]any) { c["exec"] = "powershell" },
	} {
		t.Run(name, func(t *testing.T) {
			c := sampleTUN(t)
			mutate(c)
			b, _ := json.Marshal(c)
			if _, e := validateTUN(b, "safe.srs"); e == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

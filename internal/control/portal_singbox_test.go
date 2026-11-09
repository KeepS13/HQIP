package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/rules"
)

type portalSingBoxTestConfig struct {
	Inbounds []struct {
		Type       string
		Listen     string
		ListenPort int `json:"listen_port"`
		Address    []string
		AutoRoute  bool `json:"auto_route"`
	} `json:"inbounds"`
	Outbounds []struct {
		Type, Tag, Server, Password, Default string
		ServerPort                           int `json:"server_port"`
		Outbounds                            []string
		TLS                                  struct {
			Enabled, Insecure bool
			ServerName        string `json:"server_name"`
		}
	} `json:"outbounds"`
	Route struct {
		Rules                 []json.RawMessage
		RuleSet               []json.RawMessage `json:"rule_set"`
		DefaultDomainResolver string            `json:"default_domain_resolver"`
		Final                 string
	}
	DNS struct {
		Servers []json.RawMessage
		Rules   []json.RawMessage
		Final   string
	}
}

func portalSingBoxProfileFixture() model.ClientProfile {
	return model.ClientProfile{DefaultNodeID: "ipv6", Nodes: []model.ClientNode{
		{ID: "first", Name: "节点\"A", Server: "node.example.com", Port: 443, Password: "first-password", SNI: "node.example.com", Ready: true},
		{ID: "ipv6", Name: "香港 #2\n测试", Server: "2001:db8::8", Port: 8443, Password: "user:复杂 +/%?@#secret", SNI: "tls.example.com", Ready: true},
		{ID: "pending", Name: "尚未就绪", Ready: false},
	}}
}

func portalSingBoxRulesFixture() rules.Bundle {
	return rules.Bundle{
		SchemaVersion: rules.SchemaVersion,
		GeoIPCN: rules.GeoIPCN{
			Repository: "https://github.com/gaoyifan/china-operator-ip", Commit: strings.Repeat("a", 40), License: "MIT",
			IPv4SHA256: strings.Repeat("b", 64), IPv6SHA256: strings.Repeat("c", 64), CIDRs: []string{"1.0.1.0/24", "2001:250::/32"},
		},
		Rules: []rules.Rule{
			{Type: "DOMAIN", Value: "proxy.example", Outbound: "proxy"},
			{Type: "DOMAIN-SUFFIX", Value: "direct.example", Outbound: "direct"},
			{Type: "DOMAIN-KEYWORD", Value: "blocked", Outbound: "reject"},
			{Type: "IP-CIDR", Value: "203.0.113.0/24", Outbound: "reject", NoResolve: true},
			{Type: "IP-CIDR6", Value: "2001:db8::/32", Outbound: "direct"},
			{Type: "PROCESS-NAME", Value: "app.exe", Outbound: "direct"},
			{Type: "GEOIP", Value: "CN", Outbound: "direct"},
			{Type: "MATCH", Outbound: "proxy"},
		},
	}
}

func TestPortalSingBoxConfiguration(t *testing.T) {
	profile, bundle := portalSingBoxProfileFixture(), portalSingBoxRulesFixture()
	body, err := portalSingBoxSubscription(profile, bundle, "https://api.example.com:9443/path", "https://user.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var config portalSingBoxTestConfig
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Outbounds) != 4 {
		t.Fatal("unready node was not filtered")
	}
	node, selector := config.Outbounds[2], config.Outbounds[3]
	if node.Type != "hysteria2" || node.Tag != "香港 #2\n测试 · ipv6" || node.Password != profile.Nodes[1].Password || node.Server != "2001:db8::8" || node.ServerPort != 8443 || node.TLS.ServerName != "tls.example.com" || !node.TLS.Enabled || node.TLS.Insecure {
		t.Fatal("node fields, IPv6 or TLS validation changed")
	}
	if selector.Type != "selector" || selector.Default != node.Tag || len(selector.Outbounds) != 2 || selector.Outbounds[0] != node.Tag {
		t.Fatal("default node was not selected explicitly")
	}
	if len(config.Inbounds) != 2 || config.Inbounds[0].Type != "mixed" || config.Inbounds[0].Listen != "127.0.0.1" || config.Inbounds[0].ListenPort != 7890 || config.Inbounds[1].Type != "tun" || !config.Inbounds[1].AutoRoute || len(config.Inbounds[1].Address) != 2 {
		t.Fatal("profile is missing a local proxy or cross-platform TUN")
	}
	if config.Route.DefaultDomainResolver != "local" || config.Route.Final != "proxy" || config.DNS.Final != "remote" || len(config.DNS.Servers) != 2 {
		t.Fatal("DNS bootstrap or route fallback changed")
	}
	for _, host := range []string{"api.example.com", "user.example.com", "node.example.com"} {
		if !bytes.Contains(config.Route.Rules[2], []byte(host)) || !bytes.Contains(config.DNS.Rules[0], []byte(host)) {
			t.Fatal("service or node domain no longer bypasses proxy DNS and routing", host)
		}
	}
	if !bytes.Contains(config.Route.Rules[3], []byte("2001:db8::8/128")) {
		t.Fatal("node IPv6 bypass missing")
	}
	wantRules, err := bundle.RouteRules("proxy", "direct", "geoip-cn")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Route.Rules) != len(wantRules)+4 {
		t.Fatal("routing rules were dropped or added")
	}
	for i, want := range wantRules {
		var actual map[string]any
		if err = json.Unmarshal(config.Route.Rules[i+4], &actual); err != nil {
			t.Fatal(err)
		}
		wantJSON, _ := json.Marshal(want)
		actualJSON, _ := json.Marshal(actual)
		if !bytes.Equal(actualJSON, wantJSON) {
			t.Fatalf("rule %d changed order, resolution or outbound", i)
		}
	}
	if len(config.Route.RuleSet) != 1 {
		t.Fatal("missing embedded CN rule set")
	}
	var ruleSet struct {
		Type, Tag string
		Rules     []struct {
			IPCIDR []string `json:"ip_cidr"`
		}
	}
	if err = json.Unmarshal(config.Route.RuleSet[0], &ruleSet); err != nil || ruleSet.Type != "inline" || ruleSet.Tag != "geoip-cn" || len(ruleSet.Rules) != 1 || !reflect.DeepEqual(ruleSet.Rules[0].IPCIDR, bundle.GeoIPCN.CIDRs) {
		t.Fatal("CN source is not embedded intact", err)
	}
	if bytes.Contains(body, []byte(`"path":`)) && !bytes.Contains(body, []byte(`"path": "/dns-query"`)) {
		t.Fatal("profile depends on an external rules file")
	}
	if bytes.Contains(body, []byte(`"external_controller"`)) || bytes.Contains(body, []byte(`"cache_file"`)) || bytes.Contains(body, []byte("尚未就绪")) {
		t.Fatal("profile exposed an API, cached selection or unready node")
	}
}

func TestPortalSingBoxRejectsUnusableProfile(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutate         func(*model.ClientProfile)
		isDefaultError bool
	}{
		{"default unready", func(p *model.ClientProfile) { p.Nodes[1].Ready = false }, true},
		{"default missing", func(p *model.ClientProfile) { p.DefaultNodeID = "missing" }, true},
		{"no ready nodes", func(p *model.ClientProfile) { p.Nodes = nil }, true},
		{"empty server", func(p *model.ClientProfile) { p.Nodes[0].Server = "" }, false},
		{"empty password", func(p *model.ClientProfile) { p.Nodes[0].Password = "" }, false},
		{"empty sni", func(p *model.ClientProfile) { p.Nodes[0].SNI = "" }, false},
		{"invalid port", func(p *model.ClientProfile) { p.Nodes[0].Port = 65536 }, false},
		{"duplicate node", func(p *model.ClientProfile) { p.Nodes = append(p.Nodes, p.Nodes[0]) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := portalSingBoxProfileFixture()
			tc.mutate(&p)
			body, err := portalSingBoxSubscription(p, rules.Default())
			if err == nil || body != nil || errors.Is(err, errPortalDefaultNodeUnavailable) != tc.isDefaultError {
				t.Fatal("invalid profile accepted or wrong failure", err)
			}
		})
	}
}

func TestPortalSingBoxDefaultUnavailableDoesNotFallBack(t *testing.T) {
	s, _, sub, defaultNode, link := portalExportFixture(t)
	backup, err := s.Store.CreateNode(model.Node{Name: "备用节点", SSHHost: "backup.example.com", Domain: "backup.example.com", PublicIP: "203.0.113.9", Port: 443, SSHPort: 22, SSHUser: "root"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetStatus(backup.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]' WHERE id=?`, backup.ID); err != nil {
		t.Fatal(err)
	}
	template, err := s.Store.SubscriptionTemplate(sub.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	template.NodeIDs = []string{defaultNode.ID, backup.ID}
	if _, err = s.Store.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetStatus(defaultNode.ID, "pending_verification", "test", false); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"?format=singbox", "?format=singbox&download=1"} {
		w := request(s.PublicHandler(), "GET", link+suffix, nil, "", "")
		if w.Code != 503 || !strings.Contains(w.Body.String(), "默认节点尚未就绪") || strings.Contains(w.Body.String(), `"password"`) || w.Header().Get("Content-Disposition") != "" {
			t.Fatal("default unavailable must fail without a fallback or credentials", w.Code)
		}
	}
	for _, suffix := range []string{"", "?format=v2ray"} {
		if w := request(s.PublicHandler(), "GET", link+suffix, nil, "", ""); w.Code != 200 {
			t.Fatal("new Sing Box policy changed existing formats", w.Code)
		}
	}
}

func TestPortalSingBoxPinnedCoreCheck(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX")
	if binary == "" {
		t.Skip("HQIP_SINGBOX is required for the pinned sing-box parser check")
	}
	version, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil || !bytes.Contains(version, []byte("sing-box version 1.14.4")) {
		t.Fatal("expected pinned sing-box 1.14.4", err)
	}
	for _, tc := range []struct {
		name   string
		bundle rules.Bundle
	}{{"default", rules.Default()}, {"ordered-cn", portalSingBoxRulesFixture()}} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := portalSingBoxSubscription(portalSingBoxProfileFixture(), tc.bundle, "https://api.example.com", "https://user.example.com")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "profile.json")
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(binary, "check", "-c", path).CombinedOutput()
			if err != nil {
				t.Fatalf("generated profile rejected by sing-box: %v\n%s", err, output)
			}
		})
	}
}

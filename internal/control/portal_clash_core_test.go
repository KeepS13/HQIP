package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/rules"
	"gopkg.in/yaml.v3"
)

// Fetch through the subscription handler with a historical persisted bundle.
// This catches fixes applied only to new installs rather than existing users.
func mainlandClashExport(t *testing.T, format ...string) []byte {
	t.Helper()
	s, _, _, node, link := portalExportFixture(t)
	endpoint, _ := json.Marshal(model.RealityEndpoint{Port: 8443, ServerName: model.DefaultRealityServerName, PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortID: "0123456789abcdef", Flow: model.RealityVisionFlow})
	if _, err := s.Store.DB.Exec("INSERT INTO metadata(key,value) VALUES(?,?)", "vless-reality:"+node.ID, string(endpoint)); err != nil {
		t.Fatal(err)
	}
	legacy := rules.Bundle{SchemaVersion: 1, Source: rules.Source{Kind: "builtin", Name: "HQIP local networks"}, Rules: []rules.Rule{
		{Type: "DOMAIN-SUFFIX", Value: "bilibili.com", Outbound: "proxy"},
		{Type: "IP-CIDR", Value: "127.0.0.0/8", Outbound: "direct", NoResolve: true},
		{Type: "MATCH", Outbound: "proxy"},
	}}
	if _, err := s.Store.SaveRules(legacy); err != nil {
		t.Fatal(err)
	}
	before, err := s.Store.Rules()
	if err != nil {
		t.Fatal(err)
	}
	kind := "clash"
	if len(format) > 0 {
		kind = format[0]
	}
	w := request(s.PublicHandler(), "GET", link+"?format="+kind, nil, "", "")
	if w.Code != 200 {
		t.Fatal("subscription export failed", w.Code)
	}
	after, err := s.Store.Rules()
	if err != nil || after.Hash != before.Hash || after.Version != before.Version {
		t.Fatal("export rewrote persisted administrator rules", err)
	}
	return w.Body.Bytes()
}

func TestPortalClashMainlandExport(t *testing.T) {
	body := mainlandClashExport(t)
	var config struct {
		Mode  string
		Rules []string
		DNS   struct {
			Enable bool
			Policy map[string][]string `yaml:"nameserver-policy"`
		}
	}
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if config.Mode != "rule" || !config.DNS.Enable || len(config.DNS.Policy["+.bilibili.com"]) == 0 {
		t.Fatal("rule mode or direct mainland DNS missing")
	}
	for _, host := range []string{"bilibili.com", "bilivideo.com", "hdslb.com", "qq.com", "douyin.com"} {
		want := "DOMAIN-SUFFIX," + host + ",DIRECT"
		if !strings.Contains(string(body), want) {
			t.Error("missing exported direct rule", host)
		}
	}
	for _, row := range config.Rules {
		if strings.HasPrefix(row, "GEOIP,") || strings.HasPrefix(row, "GEOSITE,") || strings.HasPrefix(row, "RULE-SET,") {
			t.Fatal("export depends on external rule databases", row)
		}
	}
	if config.Rules[len(config.Rules)-1] != "MATCH,节点选择" {
		t.Fatal("overseas fallback changed")
	}
	if directory := os.Getenv("HQIP_RULES_ARTIFACTS"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "exported-clash.yaml"), body, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "exported-singbox.json"), mainlandClashExport(t, "singbox"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("export: %d bytes, %d rules, %d DNS policies", len(body), len(config.Rules), len(config.DNS.Policy))
}

// Opt in with a path-list of authentic Clash/Meta/Mihomo binaries. Each check
// starts with an empty data directory, exposing accidental GeoIP downloads.
func TestPortalClashCoreCheck(t *testing.T) {
	binaries := filepath.SplitList(os.Getenv("HQIP_CLASH_CORES"))
	if len(binaries) == 0 {
		t.Skip("HQIP_CLASH_CORES is required for real Clash parser checks")
	}
	body := mainlandClashExport(t)
	for _, binary := range binaries {
		t.Run(filepath.Base(filepath.Dir(binary)), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, binary, "-t", "-d", root, "-f", path).CombinedOutput()
			if err != nil {
				t.Fatalf("export rejected: %v\n%s", err, out)
			}
			t.Log(strings.TrimSpace(string(out)))
		})
	}
}

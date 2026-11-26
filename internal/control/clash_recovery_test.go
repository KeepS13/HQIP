package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
	"gopkg.in/yaml.v3"
)

func TestClashNodeDNSDoesNotDependOnFallback(t *testing.T) {
	dns := clashDNS(rules.Default())
	if !reflect.DeepEqual(dns["proxy-server-nameserver"], []string{"223.5.5.5", "119.29.29.29"}) {
		t.Fatal("node bootstrap still depends on potentially unreachable overseas fallback DNS")
	}
}

// Exercise the actual subscription handler, including existing saved rules that
// reject our service. Login and subscription refresh must survive a dead node.
func TestPortalServiceRecoveryExports(t *testing.T) {
	s, _, _, _, link := portalExportFixture(t)
	s.opts.PublicURL = "https://hqip.sharevps.online"
	s.opts.UserOrigin = "https://hqip.sharevps.online"
	s.opts.AdminOrigin = "https://cms-hqip.sharevps.online"
	legacy := rules.Bundle{SchemaVersion: 1, Rules: []rules.Rule{
		{Type: "DOMAIN-KEYWORD", Value: "sharevps", Outbound: "reject"},
		{Type: "MATCH", Outbound: "proxy"},
	}}
	before, err := s.Store.SaveRules(legacy)
	if err != nil {
		t.Fatal(err)
	}
	hosts := []string{"hqip.sharevps.online", "cms-hqip.sharevps.online", "challenges.cloudflare.com"}
	for _, format := range []string{"clash", "singbox"} {
		t.Run(format, func(t *testing.T) {
			w := request(s.PublicHandler(), "GET", link+"?format="+format, nil, "", "")
			if w.Code != 200 {
				t.Fatal("export failed", w.Code)
			}
			if format == "clash" {
				var config struct {
					Rules []string
					DNS   struct {
						Policy map[string][]string `yaml:"nameserver-policy"`
						Node   []string            `yaml:"proxy-server-nameserver"`
					}
				}
				if err := yaml.Unmarshal(w.Body.Bytes(), &config); err != nil {
					t.Fatal(err)
				}
				for i, host := range hosts {
					if config.Rules[i] != "DOMAIN,"+host+",DIRECT" || len(config.DNS.Policy[host]) == 0 {
						t.Error("service DNS/route is not an exact, highest-priority direct exception", host)
					}
				}
				if len(config.DNS.Node) == 0 || config.Rules[len(config.Rules)-1] != "MATCH,节点选择" {
					t.Error("node bootstrap or normal proxy fallback missing")
				}
			} else {
				var config portalSingBoxTestConfig
				if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
					t.Fatal(err)
				}
				var route struct {
					Domain   []string
					Outbound string
				}
				var dns struct {
					Domain []string
					Server string
				}
				if json.Unmarshal(config.Route.Rules[2], &route) != nil || json.Unmarshal(config.DNS.Rules[0], &dns) != nil {
					t.Fatal("missing service exceptions")
				}
				for _, host := range hosts {
					if route.Outbound != "direct" || dns.Server != "local" || !slices.Contains(route.Domain, host) || !slices.Contains(dns.Domain, host) {
						t.Error("service requires working proxy", host)
					}
				}
			}
			if dir := os.Getenv("HQIP_RULES_ARTIFACTS"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				ext := map[string]string{"clash": "yaml", "singbox": "json"}[format]
				if err := os.WriteFile(filepath.Join(dir, "recovery-"+format+"."+ext), w.Body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	after, err := s.Store.Rules()
	if err != nil || after.Hash != before.Hash || after.Version != before.Version {
		t.Fatal("export mutated persisted rules", err)
	}
}

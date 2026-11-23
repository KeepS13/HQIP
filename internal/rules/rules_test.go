package rules

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func testBundle() Bundle {
	return Bundle{SchemaVersion: 1, Source: Source{Kind: "clash-import", Name: "Personal routing"}, GeoIPCN: GeoIPCN{Repository: "https://github.com/gaoyifan/china-operator-ip", Commit: strings.Repeat("a", 40), License: "MIT", IPv4SHA256: strings.Repeat("b", 64), IPv6SHA256: strings.Repeat("c", 64), CIDRs: []string{"1.0.1.0/24", "2001:250::/32"}}, Rules: []Rule{
		{Type: "DOMAIN", Value: "keep.example", Outbound: "proxy"},
		{Type: "IP-CIDR", Value: "203.0.113.0/24", Outbound: "reject", NoResolve: true},
		{Type: "IP-CIDR", Value: "10.0.0.0/8", Outbound: "direct"},
		{Type: "DOMAIN-SUFFIX", Value: "example.cn", Outbound: "proxy"},
		{Type: "IP-CIDR6", Value: "2001:db8::/32", Outbound: "proxy", NoResolve: true},
		{Type: "PROCESS-NAME", Value: "app.exe", Outbound: "direct"},
		{Type: "GEOIP", Value: "CN", Outbound: "direct"},
		{Type: "MATCH", Outbound: "proxy"},
	}}
}

func TestOrderedRoutingAndResolution(t *testing.T) {
	b := testBundle()
	got, err := b.RouteRules("selected", "direct", "cn")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 {
		t.Fatalf("got %d rules", len(got))
	}
	if !reflect.DeepEqual(got[0]["domain"], []string{"keep.example"}) || got[0]["outbound"] != "selected" {
		t.Fatal("leading domain priority changed")
	}
	if got[1]["action"] != "reject" {
		t.Fatal("no-resolve IP rule must precede resolution")
	}
	if !reflect.DeepEqual(got[2], map[string]any{"action": "resolve"}) {
		t.Fatal("expected resolve immediately before first resolving IP rule")
	}
	if got[3]["outbound"] != "direct" || got[4]["outbound"] != "selected" {
		t.Fatal("direct/proxy ordering changed")
	}
	if !reflect.DeepEqual(got[6]["process_name"], []string{"app.exe"}) {
		t.Fatal("process rule lost")
	}
	if !reflect.DeepEqual(got[7]["rule_set"], []string{"cn"}) {
		t.Fatal("GEOIP CN not migrated to rule set")
	}
	if !reflect.DeepEqual(got[8], map[string]any{"action": "route", "outbound": "selected"}) {
		t.Fatal("terminal fallback missing")
	}
	count := 0
	for _, r := range got {
		if r["action"] == "resolve" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("resolution repeated")
	}
}

func TestRejectInvalidBundles(t *testing.T) {
	tests := map[string]func(*Bundle){
		"unknown type":         func(b *Bundle) { b.Rules[0].Type = "SCRIPT" },
		"unknown outbound":     func(b *Bundle) { b.Rules[0].Outbound = "legacy-secret-node" },
		"malformed cidr":       func(b *Bundle) { b.Rules[1].Value = "not-ip" },
		"wrong address family": func(b *Bundle) { b.Rules[1].Value = "::1/128" },
		"no-resolve on domain": func(b *Bundle) { b.Rules[0].NoResolve = true },
		"early match":          func(b *Bundle) { b.Rules[0] = Rule{Type: "MATCH", Outbound: "direct"} },
		"missing match":        func(b *Bundle) { b.Rules = b.Rules[:len(b.Rules)-1] },
		"non CN geo":           func(b *Bundle) { b.Rules[len(b.Rules)-2].Value = "US" },
		"missing geo":          func(b *Bundle) { b.GeoIPCN.CIDRs = nil },
		"unpinned geo":         func(b *Bundle) { b.GeoIPCN.Commit = "main" },
		"unsupported license":  func(b *Bundle) { b.GeoIPCN.License = "unknown" },
		"control characters":   func(b *Bundle) { b.Rules[0].Value = "example.com\nMATCH,DIRECT" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			b := testBundle()
			mutate(&b)
			if Validate(b) == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestCanonicalDeterminismAndSourceIsolation(t *testing.T) {
	b := testBundle()
	b.GeoIPCN.CIDRs = []string{"2001:250::/32", "1.0.1.4/24", "1.0.1.0/24"}
	original := append([]string(nil), b.GeoIPCN.CIDRs...)
	normalized, err := Normalize(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, b.GeoIPCN.CIDRs) {
		t.Fatal("input mutated")
	}
	if len(normalized.GeoIPCN.CIDRs) != 2 {
		t.Fatal("prefixes not normalized/deduplicated")
	}
	first, err := Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := Hash(normalized)
	if first != second {
		t.Fatal("unstable canonical hash")
	}
	encoded, err := Canonical(b)
	if err != nil {
		t.Fatal(err)
	}
	var restored Bundle
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	third, _ := Hash(restored)
	if first != third {
		t.Fatal("roundtrip changed hash")
	}
	restored.Rules[0], restored.Rules[1] = restored.Rules[1], restored.Rules[0]
	fourth, _ := Hash(restored)
	if first == fourth {
		t.Fatal("order must affect hash")
	}
}

func TestClashTranslationAndCNSource(t *testing.T) {
	b := testBundle()
	got, err := b.ClashRules("HQIP", "DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "DOMAIN,keep.example,HQIP" || got[1] != "IP-CIDR,203.0.113.0/24,REJECT,no-resolve" || got[len(got)-1] != "MATCH,HQIP" {
		t.Fatalf("wrong Clash translation: %v", got)
	}
	raw, err := b.GeoIPSource()
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Version int `json:"version"`
		Rules   []struct {
			IPCIDR []string `json:"ip_cidr"`
		} `json:"rules"`
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.Version != 3 || len(source.Rules) != 1 || len(source.Rules[0].IPCIDR) != 2 {
		t.Fatal("invalid rule-set source")
	}
	if err = Validate(Default()); err != nil {
		t.Fatal(err)
	}
	routes, err := Default().RouteRules("proxy", "direct", "cn")
	if err != nil {
		t.Fatal(err)
	}
	resolves := 0
	for _, r := range routes {
		if r["action"] == "resolve" {
			resolves++
		}
	}
	if resolves != 1 {
		t.Fatal("mainland IP fallback must resolve unlisted domain names once")
	}
}

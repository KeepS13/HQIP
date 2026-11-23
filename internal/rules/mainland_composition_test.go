package rules

import (
	"reflect"
	"testing"
)

func TestMainlandCompositionUpgradesStoredRulesWithoutMutation(t *testing.T) {
	legacy := Bundle{SchemaVersion: 1, Source: Source{Kind: "builtin", Name: "HQIP local networks"}, Rules: []Rule{
		{Type: "DOMAIN-SUFFIX", Value: "bilibili.com", Outbound: "proxy"},
		{Type: "DOMAIN", Value: "blocked.example", Outbound: "reject"},
		{Type: "MATCH", Outbound: "proxy"},
	}}
	before, _ := Canonical(legacy)
	got, err := WithMainlandBypass(legacy)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := Canonical(legacy)
	if string(before) != string(after) {
		t.Fatal("composition modified stored rule bytes/hash")
	}
	twice, err := WithMainlandBypass(got)
	if err != nil || !reflect.DeepEqual(got, twice) {
		t.Fatal("composition is not idempotent", err)
	}
	rows, err := got.ClashRules("PROXY", "DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{"www.bilibili.com": "DIRECT", "blocked.example": "REJECT", "www.google.com": "PROXY"} {
		if actual := clashDecision(t, rows, host, "203.0.113.8"); actual != want {
			t.Errorf("%s: got %s, want %s", host, actual, want)
		}
	}
	got.Rules[0].Value = "mutated.example"
	got.GeoIPCN.CIDRs[0] = "0.0.0.0/0"
	clean := Default()
	if clean.Rules[0].Value == "mutated.example" || clean.GeoIPCN.CIDRs[0] == "0.0.0.0/0" {
		t.Fatal("caller owns neither rules nor CIDRs")
	}
}

func TestMainlandDNSUsesDirectResolver(t *testing.T) {
	rows := Default().DomainDNSRules("local", "remote")
	for _, host := range []string{"bilibili.com", "hdslb.com", "bilivideo.com", "douyin.com", "qq.com", "cn"} {
		found := false
		for _, row := range rows {
			values, _ := row["domain_suffix"].([]string)
			for _, suffix := range values {
				if suffix == host && row["server"] == "local" {
					found = true
				}
			}
		}
		if !found {
			t.Error("missing direct DNS for", host)
		}
	}
}

func TestClashExpansionIsBounded(t *testing.T) {
	b := Default()
	last := b.Rules[len(b.Rules)-1]
	b.Rules = b.Rules[:len(b.Rules)-1]
	for i := 0; i < 20; i++ {
		b.Rules = append(b.Rules, Rule{Type: "GEOIP", Value: "CN", Outbound: "direct"})
	}
	b.Rules = append(b.Rules, last)
	if _, err := b.ClashRules("PROXY", "DIRECT"); err == nil {
		t.Fatal("repeated GEOIP rules caused unbounded output expansion")
	}
}

func BenchmarkMainlandClashExport(b *testing.B) {
	bundle := Default()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := bundle.ClashRules("PROXY", "DIRECT"); err != nil {
			b.Fatal(err)
		}
	}
}

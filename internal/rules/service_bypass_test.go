package rules

import (
	"reflect"
	"testing"
)

func TestServiceBypassIsExactAndUsesConfiguredOrigins(t *testing.T) {
	urls := []string{"https://USER.example:8443/path", "https://user.example/other", "https://cms.example", "http://203.0.113.9", "https://[2001:db8::9]:9443"}
	want := []string{"user.example", "cms.example", "203.0.113.9", "2001:db8::9", "challenges.cloudflare.com"}
	if got := ServiceBypassHosts(urls...); !reflect.DeepEqual(got, want) {
		t.Fatal("unexpected hosts", got)
	}
	rows := ServiceBypassRules(urls...)
	wantRows := []Rule{
		{Type: "DOMAIN", Value: "user.example", Outbound: "direct"},
		{Type: "DOMAIN", Value: "cms.example", Outbound: "direct"},
		{Type: "IP-CIDR", Value: "203.0.113.9/32", Outbound: "direct", NoResolve: true},
		{Type: "IP-CIDR6", Value: "2001:db8::9/128", Outbound: "direct", NoResolve: true},
		{Type: "DOMAIN", Value: "challenges.cloudflare.com", Outbound: "direct"},
	}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatal("service exceptions broadened to parent domains or networks", rows)
	}
	if got := ServiceBypassHosts("", "invalid", "ftp://example.com", "https://user:pass@example.com", "https://*.example.com"); len(got) != 0 {
		t.Fatal("invalid origins gained bypass rules", got)
	}
	first := OfficialServiceHosts()
	first[0] = "mutated.example"
	if OfficialServiceHosts()[0] == first[0] {
		t.Fatal("callers can mutate shared policy")
	}
}

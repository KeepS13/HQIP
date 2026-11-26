package control

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
	"gopkg.in/yaml.v3"
)

func TestClashRemoteDNSUsesExistingSelector(t *testing.T) {
	dns := clashDNS(rules.Default())
	for _, resolver := range dns["fallback"].([]string) {
		if !strings.HasSuffix(resolver, "#节点选择") {
			t.Error("uncached overseas lookup still requires direct overseas DoH", resolver)
		}
	}
}

func TestPendingRecoveryAllowsOnlyConfiguredSites(t *testing.T) {
	urls := []string{"https://hqip.sharevps.online", "https://cms-hqip.sharevps.online"}
	hosts := []string{"hqip.sharevps.online", "cms-hqip.sharevps.online", "challenges.cloudflare.com"}
	body, _, err := pendingPortalConfig("clash", urls...)
	var clash struct {
		Rules   []string
		Proxies []any
	}
	if err != nil || yaml.Unmarshal(body, &clash) != nil {
		t.Fatal("invalid pending Clash configuration", err)
	}
	want := []string{}
	for _, host := range hosts {
		want = append(want, "DOMAIN,"+host+",DIRECT")
	}
	want = append(want, "MATCH,REJECT")
	if !reflect.DeepEqual(clash.Rules, want) || len(clash.Proxies) != 0 {
		t.Fatal("pending profile grants general access or blocks recovery", clash.Rules)
	}
	body, _, err = pendingPortalConfig("singbox", urls...)
	var sing struct {
		Outbounds []struct{ Type string }
		Route     struct {
			Rules []struct {
				Domain           []string
				Action, Outbound string
			}
		}
	}
	if err != nil || json.Unmarshal(body, &sing) != nil {
		t.Fatal("invalid pending sing-box profile", err)
	}
	if len(sing.Outbounds) != 1 || sing.Outbounds[0].Type != "direct" || len(sing.Route.Rules) != 4 {
		t.Fatal("pending profile contains proxy credentials or unexpected rules")
	}
	for i, host := range hosts {
		r := sing.Route.Rules[i]
		if !reflect.DeepEqual(r.Domain, []string{host}) || r.Outbound != "direct" || r.Action != "route" {
			t.Error("pending recovery is not an exact domain exception", host)
		}
	}
	if sing.Route.Rules[3].Action != "reject" {
		t.Error("general pending traffic must remain rejected")
	}
}

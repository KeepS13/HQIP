package rules

import (
	"fmt"
	"strings"
)

// ClashRules preserves ordered policies using legacy-compatible scalar rules.
// CN IP data is inlined, so missing/stale client MMDB files cannot change routing.
func (b Bundle) ClashRules(proxyTag, directTag string) ([]string, error) {
	b, err := Normalize(b)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(proxyTag+directTag, ",\r\n") || proxyTag == "" || directTag == "" {
		return nil, fmt.Errorf("invalid Clash policy tags")
	}
	count := len(b.Rules)
	for _, r := range b.Rules {
		if r.Type == "GEOIP" {
			count += len(b.GeoIPCN.CIDRs) - 1
		}
		if count > 100000 {
			return nil, fmt.Errorf("Clash export exceeds 100000 expanded rules")
		}
	}
	out := make([]string, 0, count)
	for _, r := range b.Rules {
		policy := proxyTag
		if r.Outbound == "direct" {
			policy = directTag
		} else if r.Outbound == "reject" {
			policy = "REJECT"
		}
		if r.Type == "GEOIP" {
			for _, cidr := range b.GeoIPCN.CIDRs {
				typ := "IP-CIDR"
				if strings.Contains(cidr, ":") {
					typ = "IP-CIDR6"
				}
				out = append(out, clashScalar(Rule{Type: typ, Value: cidr, NoResolve: r.NoResolve}, policy))
			}
			continue
		}
		out = append(out, clashScalar(r, policy))
	}
	return out, nil
}

func clashScalar(r Rule, policy string) string {
	parts := []string{r.Type}
	if r.Type != "MATCH" {
		parts = append(parts, r.Value)
	}
	parts = append(parts, policy)
	if r.NoResolve {
		parts = append(parts, "no-resolve")
	}
	return strings.Join(parts, ",")
}

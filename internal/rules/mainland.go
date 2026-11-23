package rules

import (
	"embed"
	"encoding/json"
	"fmt"
)

// The credential-free snapshot is compiled into the program. Requests never
// fetch rule providers; provenance and upstream licenses live beside the data.
//
//go:embed data/*
var mainlandFiles embed.FS

var mainland = func() Bundle {
	var b Bundle
	data, err := mainlandFiles.ReadFile("data/mainland.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &b); err != nil {
		panic("invalid embedded mainland routing data: " + err.Error())
	}
	return b
}()

// WithMainlandBypass returns an independent effective bundle. The mainland
// policy precedes administrator rules, including old service groups that sent
// Bilibili through a node. Other custom rules retain their order and final action.
// Composition happens at configuration generation, so persisted LAN-only bundles
// are fixed on subscription refresh without rewriting rule history or hashes.
func WithMainlandBypass(b Bundle) (Bundle, error) {
	b, err := Normalize(b)
	if err != nil {
		return Bundle{}, err
	}
	rows := make([]Rule, 0, len(mainland.Rules)+len(b.Rules)+1)
	seen := make(map[Rule]bool, cap(rows))
	add := func(r Rule) {
		key := r
		key.SourcePolicy = ""
		if !seen[key] {
			rows = append(rows, r)
			seen[key] = true
		}
	}
	// These names must be matched before the resolving CN IP guard: a failed
	// lookup of a private suffix must not prevent its explicit direct rule.
	for _, r := range b.Rules {
		if r.Type == "DOMAIN-SUFFIX" && r.Outbound == "direct" && (r.Value == "localhost" || r.Value == "local" || r.Value == "lan") {
			add(r)
		}
	}
	for _, r := range mainland.Rules {
		add(r)
	}
	// Resolution is essential for unlisted domains hosted on a mainland IP.
	add(Rule{Type: "GEOIP", Value: "CN", Outbound: "direct"})
	for _, r := range b.Rules {
		add(r)
	}
	b.Rules = rows
	b.GeoIPCN = mainland.GeoIPCN
	b.GeoIPCN.CIDRs = append([]string(nil), mainland.GeoIPCN.CIDRs...)
	if len(b.Rules) > 40000 {
		return Bundle{}, fmt.Errorf("effective mainland routing exceeds 40000 rules")
	}
	return b, nil
}

// DomainDNSRules routes DNS consistently with the effective domain rules. IP
// classification uses the direct resolver at the call site; otherwise a remote
// resolver could choose a foreign CDN before the mainland IP test even runs.
func (b Bundle) DomainDNSRules(direct, remote string) []map[string]any {
	rows := []map[string]any{}
	for _, r := range b.Rules {
		key := map[string]string{"DOMAIN": "domain", "DOMAIN-SUFFIX": "domain_suffix", "DOMAIN-KEYWORD": "domain_keyword"}[r.Type]
		if key == "" || r.Outbound == "reject" {
			continue
		}
		server := remote
		if r.Outbound == "direct" {
			server = direct
		}
		// Only adjacent equivalent rules merge; first-match ordering is retained.
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			if values, ok := last[key].([]string); ok && last["server"] == server {
				last[key] = append(values, r.Value)
				continue
			}
		}
		rows = append(rows, map[string]any{key: []string{r.Value}, "server": server})
	}
	return rows
}

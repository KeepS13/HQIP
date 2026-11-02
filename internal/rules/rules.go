// Package rules validates and translates credential-free, ordered routing bundles.
package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"unicode"
)

const SchemaVersion = 1

type Source struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	ImportedAt string `json:"importedAt"`
}

type Rule struct {
	Type         string `json:"type"`
	Value        string `json:"value,omitempty"`
	Outbound     string `json:"outbound"`
	NoResolve    bool   `json:"noResolve,omitempty"`
	SourcePolicy string `json:"sourcePolicy,omitempty"`
}

type GeoIPCN struct {
	Repository string   `json:"repository"`
	Commit     string   `json:"commit"`
	License    string   `json:"license"`
	IPv4SHA256 string   `json:"ipv4SHA256"`
	IPv6SHA256 string   `json:"ipv6SHA256"`
	CIDRs      []string `json:"cidrs"`
}

type Bundle struct {
	SchemaVersion int     `json:"schemaVersion"`
	Source        Source  `json:"source"`
	Rules         []Rule  `json:"rules"`
	GeoIPCN       GeoIPCN `json:"geoipCN"`
}

func Default() Bundle {
	b := Bundle{SchemaVersion: SchemaVersion, Source: Source{Kind: "builtin", Name: "HQIP local networks"}, Rules: []Rule{}, GeoIPCN: GeoIPCN{CIDRs: []string{}}}
	for _, domain := range []string{"localhost", "local", "lan"} {
		b.Rules = append(b.Rules, Rule{Type: "DOMAIN-SUFFIX", Value: domain, Outbound: "direct"})
	}
	for _, cidr := range []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "::1/128", "fc00::/7", "fe80::/10"} {
		typ := "IP-CIDR"
		if strings.Contains(cidr, ":") {
			typ = "IP-CIDR6"
		}
		b.Rules = append(b.Rules, Rule{Type: typ, Value: cidr, Outbound: "direct", NoResolve: true})
	}
	b.Rules = append(b.Rules, Rule{Type: "MATCH", Outbound: "proxy"})
	return b
}

// Normalize returns an independent canonical bundle. Rule ordering is never changed.
func Normalize(b Bundle) (Bundle, error) {
	if b.SchemaVersion != SchemaVersion {
		return Bundle{}, fmt.Errorf("unsupported routing schema version: %d", b.SchemaVersion)
	}
	if len(b.Rules) == 0 || len(b.Rules) > 20000 {
		return Bundle{}, fmt.Errorf("routing bundle must contain 1 to 20000 rules")
	}
	b.Rules = append([]Rule(nil), b.Rules...)
	for _, s := range []string{b.Source.Kind, b.Source.Name, b.Source.ImportedAt} {
		if len(s) > 256 || hasControl(s) {
			return Bundle{}, fmt.Errorf("invalid routing source metadata")
		}
	}
	if b.Source.SHA256 != "" && !hexDigest(b.Source.SHA256, 64) {
		return Bundle{}, fmt.Errorf("invalid source SHA256")
	}
	needsCN := false
	for i := range b.Rules {
		r := &b.Rules[i]
		r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
		r.Value = strings.TrimSpace(r.Value)
		r.Outbound = strings.TrimSpace(r.Outbound)
		if r.Outbound != "direct" && r.Outbound != "proxy" && r.Outbound != "reject" {
			return Bundle{}, fmt.Errorf("rule %d: unknown outbound", i+1)
		}
		if len(r.SourcePolicy) > 128 || hasControl(r.SourcePolicy) {
			return Bundle{}, fmt.Errorf("rule %d: invalid source policy", i+1)
		}
		if len(r.Value) > 1024 || hasControl(r.Value) || strings.Contains(r.Value, ",") {
			return Bundle{}, fmt.Errorf("rule %d: invalid value", i+1)
		}
		isIP := r.Type == "IP-CIDR" || r.Type == "IP-CIDR6" || r.Type == "GEOIP"
		if r.NoResolve && !isIP {
			return Bundle{}, fmt.Errorf("rule %d: no-resolve requires an IP rule", i+1)
		}
		switch r.Type {
		case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD":
			if r.Value == "" || strings.ContainsAny(r.Value, " /\\:*?#@") {
				return Bundle{}, fmt.Errorf("rule %d: invalid domain value", i+1)
			}
			r.Value = strings.ToLower(r.Value)
		case "PROCESS-NAME":
			if r.Value == "" || strings.ContainsAny(r.Value, "/\\") {
				return Bundle{}, fmt.Errorf("rule %d: invalid process name", i+1)
			}
		case "IP-CIDR", "IP-CIDR6":
			prefix, err := netip.ParsePrefix(r.Value)
			if err != nil || prefix.Addr().Is4() != (r.Type == "IP-CIDR") {
				return Bundle{}, fmt.Errorf("rule %d: invalid IP prefix", i+1)
			}
			r.Value = prefix.Masked().String()
		case "GEOIP":
			if !strings.EqualFold(r.Value, "CN") {
				return Bundle{}, fmt.Errorf("rule %d: only GEOIP CN is supported", i+1)
			}
			r.Value = "CN"
			needsCN = true
		case "MATCH":
			if r.Value != "" || i != len(b.Rules)-1 {
				return Bundle{}, fmt.Errorf("MATCH must be the final rule and have no value")
			}
		default:
			return Bundle{}, fmt.Errorf("rule %d: unsupported type %q", i+1, r.Type)
		}
	}
	if b.Rules[len(b.Rules)-1].Type != "MATCH" {
		return Bundle{}, fmt.Errorf("final MATCH rule is required")
	}
	if len(b.GeoIPCN.CIDRs) > 100000 {
		return Bundle{}, fmt.Errorf("too many GeoIP CN prefixes")
	}
	if needsCN || len(b.GeoIPCN.CIDRs) > 0 {
		if b.GeoIPCN.Repository != "https://github.com/gaoyifan/china-operator-ip" || b.GeoIPCN.License != "MIT" || !hexDigest(b.GeoIPCN.Commit, 40) || !hexDigest(b.GeoIPCN.IPv4SHA256, 64) || !hexDigest(b.GeoIPCN.IPv6SHA256, 64) {
			return Bundle{}, fmt.Errorf("GeoIP CN requires pinned gaoyifan source and MIT license metadata")
		}
		if len(b.GeoIPCN.CIDRs) == 0 {
			return Bundle{}, fmt.Errorf("GEOIP CN requires nonempty prefixes")
		}
	}
	prefixes := make(map[string]bool, len(b.GeoIPCN.CIDRs))
	b.GeoIPCN.CIDRs = append([]string{}, b.GeoIPCN.CIDRs...)
	for _, value := range b.GeoIPCN.CIDRs {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return Bundle{}, fmt.Errorf("invalid GeoIP CN prefix")
		}
		prefixes[prefix.Masked().String()] = true
	}
	b.GeoIPCN.CIDRs = b.GeoIPCN.CIDRs[:0]
	for value := range prefixes {
		b.GeoIPCN.CIDRs = append(b.GeoIPCN.CIDRs, value)
	}
	sort.Strings(b.GeoIPCN.CIDRs)
	return b, nil
}

func Validate(b Bundle) error { _, err := Normalize(b); return err }

// Canonical is the exact representation served to clients and covered by Hash.
func Canonical(b Bundle) ([]byte, error) {
	b, err := Normalize(b)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(b); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func Hash(b Bundle) (string, error) {
	data, err := Canonical(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// RouteRules emits ordered sing-box 1.14 route rules, including the terminal MATCH.
// A single resolve action is inserted immediately before the first IP rule which
// requests resolution. Later no-resolve rules may match that already-known IP.
func (b Bundle) RouteRules(proxyTag, directTag, geoTag string) ([]map[string]any, error) {
	b, err := Normalize(b)
	if err != nil {
		return nil, err
	}
	if proxyTag == "" || directTag == "" {
		return nil, fmt.Errorf("outbound tags are required")
	}
	result := make([]map[string]any, 0, len(b.Rules)+1)
	resolved := false
	for _, r := range b.Rules {
		isIP := r.Type == "IP-CIDR" || r.Type == "IP-CIDR6" || r.Type == "GEOIP"
		if isIP && !r.NoResolve && !resolved {
			result = append(result, map[string]any{"action": "resolve"})
			resolved = true
		}
		item := map[string]any{"action": "route", "outbound": proxyTag}
		if r.Outbound == "direct" {
			item["outbound"] = directTag
		}
		if r.Outbound == "reject" {
			item = map[string]any{"action": "reject"}
		}
		switch r.Type {
		case "DOMAIN":
			item["domain"] = []string{r.Value}
		case "DOMAIN-SUFFIX":
			item["domain_suffix"] = []string{r.Value}
		case "DOMAIN-KEYWORD":
			item["domain_keyword"] = []string{r.Value}
		case "PROCESS-NAME":
			item["process_name"] = []string{r.Value}
		case "IP-CIDR", "IP-CIDR6":
			item["ip_cidr"] = []string{r.Value}
		case "GEOIP":
			if geoTag == "" {
				return nil, fmt.Errorf("GeoIP CN rule-set tag is required")
			}
			item["rule_set"] = []string{geoTag}
		}
		result = append(result, item)
	}
	return result, nil
}

func (b Bundle) GeoIPSource() ([]byte, error) {
	b, err := Normalize(b)
	if err != nil {
		return nil, err
	}
	if len(b.GeoIPCN.CIDRs) == 0 {
		return nil, fmt.Errorf("GeoIP CN prefixes are unavailable")
	}
	return json.Marshal(map[string]any{"version": 3, "rules": []map[string]any{{"ip_cidr": b.GeoIPCN.CIDRs}}})
}

func (b Bundle) ClashRules(proxyTag, directTag string) ([]string, error) {
	b, err := Normalize(b)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(proxyTag+directTag, ",\r\n") || proxyTag == "" || directTag == "" {
		return nil, fmt.Errorf("invalid Clash policy tags")
	}
	out := make([]string, 0, len(b.Rules))
	for _, r := range b.Rules {
		policy := proxyTag
		if r.Outbound == "direct" {
			policy = directTag
		} else if r.Outbound == "reject" {
			policy = "REJECT"
		}
		parts := []string{r.Type}
		if r.Type != "MATCH" {
			parts = append(parts, r.Value)
		}
		parts = append(parts, policy)
		if r.NoResolve {
			parts = append(parts, "no-resolve")
		}
		out = append(out, strings.Join(parts, ","))
	}
	return out, nil
}

func hasControl(value string) bool { return strings.IndexFunc(value, unicode.IsControl) >= 0 }
func hexDigest(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

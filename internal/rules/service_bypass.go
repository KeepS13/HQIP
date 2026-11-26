package rules

import (
	"net/netip"
	"net/url"
	"strings"
)

// OfficialServiceHosts owns the native client's recovery allowlist. Return a
// fresh slice so configuration builders cannot mutate another client's policy.
// Custom installations use ServiceBypassHosts with their configured URLs.
func OfficialServiceHosts() []string {
	return ServiceBypassHosts("https://hqip.sharevps.online", "https://cms-hqip.sharevps.online")
}

// ServiceBypassHosts returns exact, deduplicated website/API hosts, followed by
// the login widget's host. A failed node must not block login, refresh or the
// Turnstile challenge. This is routing policy, never an authentication allowlist.
// Invalid/non-HTTP URLs are ignored; no parent domain or CDN IP range is allowed.
func ServiceBypassHosts(serviceURLs ...string) []string {
	hosts := []string{}
	seen := map[string]bool{}
	for _, raw := range serviceURLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			continue
		}
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		if host == "" || strings.ContainsAny(host, " ,/\\*?#@\r\n\t") || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	if len(hosts) > 0 && !seen["challenges.cloudflare.com"] {
		hosts = append(hosts, "challenges.cloudflare.com")
	}
	return hosts
}

// ServiceBypassRules must precede user rules and IP classification. Only URL
// literals become /32 or /128 rules: resolving shared CDN addresses here would
// bypass unrelated sites and require a network request during every export.
func ServiceBypassRules(serviceURLs ...string) []Rule {
	rows := []Rule{}
	for _, host := range ServiceBypassHosts(serviceURLs...) {
		r := Rule{Type: "DOMAIN", Value: host, Outbound: "direct"}
		if ip, err := netip.ParseAddr(host); err == nil {
			r.Type, r.Value, r.NoResolve = "IP-CIDR6", netip.PrefixFrom(ip, ip.BitLen()).String(), true
			if ip.Is4() {
				r.Type = "IP-CIDR"
			}
		}
		rows = append(rows, r)
	}
	return rows
}

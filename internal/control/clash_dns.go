package control

import (
	"github.com/KeepS13/HQIP/internal/rules"
)

// clashDNS uses fields supported by legacy Clash/Meta. Redir-host avoids fake-IP
// exceptions for LAN discovery and media apps. Explicit mainland DNS policies
// take priority over fallback, so domestic CDN lookup never uses overseas DNS.
// No GEOIP/GEOSITE or provider download is needed to start this configuration.
// Node bootstrap must use reachable direct resolvers independently of fallback:
// otherwise a timed-out overseas DoH resolver prevents either protocol dialing.
// Overseas DoH uses the existing node selector after that bootstrap, avoiding a
// five-second direct-DNS timeout before each uncached overseas website request.
func clashDNS(bundle rules.Bundle) map[string]any {
	local := []string{"223.5.5.5", "119.29.29.29"}
	policy := map[string]any{}
	for _, r := range bundle.Rules {
		if r.Outbound != "direct" {
			continue
		}
		switch r.Type {
		case "DOMAIN":
			policy[r.Value] = local
		case "DOMAIN-SUFFIX":
			policy["+."+r.Value] = local
		}
	}
	// Private names belong to the user's LAN resolver, not a public DNS service.
	// "system" is supported by the oldest Meta core in the compatibility matrix.
	for _, suffix := range []string{"localhost", "local", "lan"} {
		policy["+."+suffix] = []string{"system"}
	}
	return map[string]any{
		"enable": true, "ipv6": true, "enhanced-mode": "redir-host",
		"default-nameserver": local, "nameserver": local,
		"proxy-server-nameserver": local,
		"nameserver-policy":       policy,
		"fallback":                []string{"https://1.1.1.1/dns-query#节点选择", "https://8.8.8.8/dns-query#节点选择"},
		"fallback-filter":         map[string]any{"geoip": false, "ipcidr": []string{"0.0.0.0/0", "::/0"}},
	}
}

// Apps using their own DNS may give the proxy an IP rather than a hostname.
// Recover HTTP Host/TLS SNI names for classification while retaining the
// original destination, so sniffing does not silently choose a different CDN.
func clashSniffer() map[string]any {
	return map[string]any{
		"enable": true, "parse-pure-ip": true, "force-dns-mapping": true,
		"override-destination": false,
		"sniff": map[string]any{
			"HTTP": map[string]any{"ports": []any{80, "8080-8880"}},
			"TLS":  map[string]any{"ports": []int{443, 8443}},
		},
	}
}

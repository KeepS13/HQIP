package winclient

import (
	"errors"
)

// validateDNSConfig limits DNS to the two configured transports and domain matchers.
// Rule data may choose local/remote resolution but cannot add files or listeners.
func validateDNSConfig(c map[string]any) error {
	dns, e := object(c["dns"])
	if e != nil {
		return e
	}
	if e = keys(dns, "servers", "rules", "final", "strategy"); e != nil {
		return e
	}
	if dns["final"] != "remote" || dns["strategy"] != "prefer_ipv4" {
		return errors.New("DNS 参数无效")
	}
	servers, e := array(dns["servers"])
	if e != nil || len(servers) != 2 {
		return errors.New("DNS 服务器无效")
	}
	for _, raw := range servers {
		m, e := object(raw)
		if e != nil {
			return e
		}
		if e = keys(m, "type", "tag", "server", "server_port", "path", "detour"); e != nil {
			return e
		}
		if m["type"] == "local" {
			if m["tag"] != "local" || len(m) != 2 {
				return errors.New("本地 DNS 无效")
			}
		} else if m["type"] != "https" || m["tag"] != "remote" || m["server"] != "1.1.1.1" || m["server_port"] != float64(443) || m["path"] != "/dns-query" || m["detour"] != "proxy" {
			return errors.New("远程 DNS 无效")
		}
	}
	dnsRules, e := array(dns["rules"])
	if e != nil || len(dnsRules) > 41000 {
		return errors.New("DNS 规则数量无效")
	}
	for _, raw := range dnsRules {
		m, e := object(raw)
		if e != nil {
			return e
		}
		if e = keys(m, "domain", "domain_suffix", "domain_keyword", "server"); e != nil {
			return e
		}
		if len(m) != 2 || (m["server"] != "local" && m["server"] != "remote") {
			return errors.New("DNS 匹配字段无效")
		}
		for key, value := range m {
			if key != "server" && !texts(value) {
				return errors.New("DNS 域名匹配无效")
			}
		}
	}
	return nil
}

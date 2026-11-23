package winclient

import (
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
)

// validateRoutingConfig accepts only declarative routing and the one CN rule set.
// Client paths are replaced with the helper-owned path; inline data has no files.
func validateRoutingConfig(c map[string]any, seen map[string]bool, geoPath string) error {
	route, e := object(c["route"])
	if e != nil {
		return e
	}
	if e = keys(route, "auto_detect_interface", "default_domain_resolver", "rules", "final", "rule_set"); e != nil {
		return e
	}
	if route["auto_detect_interface"] != true || route["default_domain_resolver"] != "local" || route["final"] != "proxy" {
		return errors.New("路由参数无效")
	}
	rules, e := array(route["rules"])
	if e != nil || len(rules) > 41000 {
		return errors.New("路由数量无效")
	}
	for _, raw := range rules {
		m, e := object(raw)
		if e != nil {
			return e
		}
		if e = keys(m, "action", "protocol", "process_name", "domain", "domain_suffix", "domain_keyword", "ip_is_private", "ip_cidr", "rule_set", "outbound", "inbound", "clash_mode", "server"); e != nil {
			return e
		}
		if server, exists := m["server"]; exists && (server != "local" || m["action"] != "resolve" || len(m) != 2) {
			return errors.New("仅允许显式使用本地 DNS 的解析动作")
		}
		if mode, exists := m["clash_mode"]; exists {
			tags, _ := json.Marshal(m["inbound"])
			if len(m) != 3 || mode != "hqip-paused" || m["action"] != "reject" || string(tags) != `["mixed-in","tun-in"]` {
				return errors.New("转发暂停规则无效")
			}
		}
		switch text(m["action"]) {
		case "sniff", "hijack-dns", "resolve", "reject", "route":
		default:
			return errors.New("路由动作无效")
		}
		if o, ok := m["outbound"]; ok && o != "proxy" && o != "direct" {
			return errors.New("路由出口无效")
		}
		if inbound, exists := m["inbound"]; exists {
			if !texts(inbound) {
				return errors.New("入口匹配无效")
			}
			for _, tag := range inbound.([]any) {
				if !seen[text(tag)] {
					return errors.New("路由引用未知入口")
				}
			}
		}
		if _, exists := m["process_name"]; exists {
			b, _ := json.Marshal(m["inbound"])
			if string(b) != `["tun-in"]` {
				return errors.New("进程直连规则仅可用于 TUN 入口")
			}
		}
		for k, v := range m {
			switch k {
			case "action", "outbound", "protocol", "clash_mode", "server":
				if _, ok := v.(string); !ok {
					return errors.New("路由字符串无效")
				}
			case "ip_is_private":
				if _, ok := v.(bool); !ok {
					return errors.New("私网规则无效")
				}
			default:
				if !texts(v) {
					return errors.New("路由匹配字段无效")
				}
			}
		}
	}
	if seen["probe-in"] {
		if len(rules) == 0 {
			return errors.New("缺少探测入口规则")
		}
		first, _ := object(rules[0])
		b, _ := json.Marshal(first["inbound"])
		if string(b) != `["probe-in"]` || first["action"] != "route" || first["outbound"] != "proxy" {
			return errors.New("探测入口必须直接验证所选代理")
		}
	}
	sets, e := array(route["rule_set"])
	if e != nil || len(sets) > 1 {
		return errors.New("规则集合无效")
	}
	if len(sets) == 1 {
		rs, e := object(sets[0])
		if e != nil {
			return e
		}
		if rs["type"] == "inline" {
			if e = validateInlineCN(rs); e != nil {
				return e
			}
		} else {
			if e = keys(rs, "type", "tag", "format", "path"); e != nil {
				return e
			}
			if rs["type"] != "local" || rs["tag"] != "geoip-cn" || rs["format"] != "binary" {
				return errors.New("仅支持本地 GeoIP 规则")
			}
			rs["path"] = filepath.ToSlash(geoPath)
		}
	}
	return nil
}

// Inline CN data can only contain literal prefixes, not nested actions or file
// references. The same 100,000-prefix ceiling is used by the rule bundle parser.
func validateInlineCN(rs map[string]any) error {
	if err := keys(rs, "type", "tag", "rules"); err != nil {
		return err
	}
	rows, err := array(rs["rules"])
	if err != nil || rs["tag"] != "geoip-cn" || len(rows) != 1 {
		return errors.New("内嵌 GeoIP 规则无效")
	}
	row, err := object(rows[0])
	if err != nil || len(row) != 1 || !texts(row["ip_cidr"]) {
		return errors.New("内嵌 GeoIP 匹配无效")
	}
	prefixes := row["ip_cidr"].([]any)
	if len(prefixes) == 0 || len(prefixes) > 100000 {
		return errors.New("内嵌 GeoIP 数量无效")
	}
	for _, value := range prefixes {
		if _, err := netip.ParsePrefix(text(value)); err != nil {
			return errors.New("内嵌 GeoIP 地址无效")
		}
	}
	return nil
}

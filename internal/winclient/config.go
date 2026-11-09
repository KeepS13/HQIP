package winclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
)

const coreSHA256 = "8f30682ee198855440d2a7fe070d5d3653793b2d01bb8c85eea88f93bbe7c8b9"
const maxMessage = 20 << 20

type helperRequest struct {
	Command string `json:"command"`
	Config  []byte `json:"config,omitempty"`
	Geo     []byte `json:"geo,omitempty"`
}
type helperResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Running bool   `json:"running"`
}

func keys(m map[string]any, allowed ...string) error {
	set := map[string]bool{}
	for _, k := range allowed {
		set[k] = true
	}
	for k := range m {
		if !set[k] {
			return fmt.Errorf("TUN 配置含不允许的字段: %s", k)
		}
	}
	return nil
}
func object(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("TUN 配置对象无效")
	}
	return m, nil
}
func array(v any) ([]any, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, errors.New("TUN 配置数组无效")
	}
	return a, nil
}
func text(v any) string { s, _ := v.(string); return s }
func texts(v any) bool {
	a, ok := v.([]any)
	if !ok {
		return false
	}
	for _, v := range a {
		s, ok := v.(string)
		if !ok || len(s) > 2048 || strings.ContainsAny(s, "\x00\r\n") {
			return false
		}
	}
	return true
}
func port(v any) bool {
	f, ok := v.(float64)
	return ok && f >= 1 && f <= 65535 && f == float64(int(f))
}

// Validate the complete privileged input surface. It permits no scripts, plugins,
// arbitrary files, log outputs, listeners on the LAN, or TLS verification bypass.
func validateTUN(config []byte, geoPath string) ([]byte, error) {
	if len(config) > 8<<20 {
		return nil, errors.New("TUN 配置过大")
	}
	var c map[string]any
	d := json.NewDecoder(bytes.NewReader(config))
	if err := d.Decode(&c); err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("多余的 TUN 配置数据")
	}
	if err := keys(c, "log", "dns", "inbounds", "outbounds", "route", "experimental"); err != nil {
		return nil, err
	}
	log, e := object(c["log"])
	if e != nil {
		return nil, e
	}
	if e = keys(log, "level", "timestamp"); e != nil {
		return nil, e
	}
	if log["level"] != "error" {
		return nil, errors.New("TUN 日志级别无效")
	}
	ins, e := array(c["inbounds"])
	if e != nil || len(ins) < 2 || len(ins) > 3 {
		return nil, errors.New("TUN 入口数量无效")
	}
	seen := map[string]bool{}
	for _, raw := range ins {
		m, e := object(raw)
		if e != nil {
			return nil, e
		}
		typ := text(m["type"])
		tag := text(m["tag"])
		if seen[tag] {
			return nil, errors.New("TUN 入口重复")
		}
		seen[tag] = true
		switch typ {
		case "mixed":
			if e = keys(m, "type", "tag", "listen", "listen_port", "set_system_proxy"); e != nil {
				return nil, e
			}
			if (tag != "mixed-in" && tag != "probe-in") || m["listen"] != "127.0.0.1" || !port(m["listen_port"]) || m["set_system_proxy"] != false {
				return nil, errors.New("代理入口必须仅监听本机")
			}
		case "tun":
			if e = keys(m, "type", "tag", "interface_name", "address", "auto_route", "strict_route", "stack"); e != nil {
				return nil, e
			}
			b, _ := json.Marshal(m["address"])
			if tag != "tun-in" || m["interface_name"] != "HQIP" || string(b) != `["172.28.91.1/30","fdfe:dcba:9876::1/126"]` || m["auto_route"] != true || m["strict_route"] != true || m["stack"] != "mixed" {
				return nil, errors.New("TUN 接口参数无效")
			}
		default:
			return nil, errors.New("不支持此入口")
		}
	}
	if !seen["mixed-in"] || !seen["tun-in"] {
		return nil, errors.New("缺少 TUN 入口")
	}
	outs, e := array(c["outbounds"])
	if e != nil || len(outs) < 3 || len(outs) > 1002 {
		return nil, errors.New("节点数量无效")
	}
	tags := map[string]bool{}
	var selector map[string]any
	for _, raw := range outs {
		m, e := object(raw)
		if e != nil {
			return nil, e
		}
		tag := text(m["tag"])
		if tag == "" || tags[tag] {
			return nil, errors.New("出口标识无效")
		}
		tags[tag] = true
		switch m["type"] {
		case "direct":
			if e = keys(m, "type", "tag"); e != nil {
				return nil, e
			}
			if tag != "direct" {
				return nil, errors.New("直连标识无效")
			}
		case "hysteria2":
			if e = keys(m, "type", "tag", "server", "server_port", "password", "tls"); e != nil {
				return nil, e
			}
			if !strings.HasPrefix(tag, "node-") || !port(m["server_port"]) || text(m["server"]) == "" || len(text(m["password"])) < 16 {
				return nil, errors.New("节点参数无效")
			}
			tls, e := object(m["tls"])
			if e != nil {
				return nil, e
			}
			if e = keys(tls, "enabled", "server_name", "insecure"); e != nil {
				return nil, e
			}
			if tls["enabled"] != true || tls["insecure"] != false || text(tls["server_name"]) == "" {
				return nil, errors.New("必须启用 TLS 验证")
			}
		case "selector":
			if e = keys(m, "type", "tag", "outbounds", "default", "interrupt_exist_connections"); e != nil {
				return nil, e
			}
			if tag != "proxy" || m["interrupt_exist_connections"] != true || !texts(m["outbounds"]) {
				return nil, errors.New("节点选择器无效")
			}
			selector = m
		default:
			return nil, errors.New("不支持此出口")
		}
	}
	if !tags["direct"] || selector == nil {
		return nil, errors.New("缺少标准出口")
	}
	for _, v := range selector["outbounds"].([]any) {
		if !strings.HasPrefix(text(v), "node-") || !tags[text(v)] {
			return nil, errors.New("选择器引用未知节点")
		}
	}
	if !strings.HasPrefix(text(selector["default"]), "node-") || !tags[text(selector["default"])] {
		return nil, errors.New("默认节点无效")
	}
	route, e := object(c["route"])
	if e != nil {
		return nil, e
	}
	if e = keys(route, "auto_detect_interface", "default_domain_resolver", "rules", "final", "rule_set"); e != nil {
		return nil, e
	}
	if route["auto_detect_interface"] != true || route["default_domain_resolver"] != "local" || route["final"] != "proxy" {
		return nil, errors.New("路由参数无效")
	}
	rules, e := array(route["rules"])
	if e != nil || len(rules) > 21000 {
		return nil, errors.New("路由数量无效")
	}
	for _, raw := range rules {
		m, e := object(raw)
		if e != nil {
			return nil, e
		}
		if e = keys(m, "action", "protocol", "process_name", "domain", "domain_suffix", "domain_keyword", "ip_is_private", "ip_cidr", "rule_set", "outbound", "inbound", "clash_mode"); e != nil {
			return nil, e
		}
		if mode, exists := m["clash_mode"]; exists {
			tags, _ := json.Marshal(m["inbound"])
			if len(m) != 3 || mode != "hqip-paused" || m["action"] != "reject" || string(tags) != `["mixed-in","tun-in"]` {
				return nil, errors.New("转发暂停规则无效")
			}
		}
		switch text(m["action"]) {
		case "sniff", "hijack-dns", "resolve", "reject", "route":
		default:
			return nil, errors.New("路由动作无效")
		}
		if o, ok := m["outbound"]; ok && o != "proxy" && o != "direct" {
			return nil, errors.New("路由出口无效")
		}
		if inbound, exists := m["inbound"]; exists {
			if !texts(inbound) {
				return nil, errors.New("入口匹配无效")
			}
			for _, tag := range inbound.([]any) {
				if !seen[text(tag)] {
					return nil, errors.New("路由引用未知入口")
				}
			}
		}
		if _, exists := m["process_name"]; exists {
			b, _ := json.Marshal(m["inbound"])
			if string(b) != `["tun-in"]` {
				return nil, errors.New("进程直连规则仅可用于 TUN 入口")
			}
		}
		for k, v := range m {
			switch k {
			case "action", "outbound", "protocol", "clash_mode":
				if _, ok := v.(string); !ok {
					return nil, errors.New("路由字符串无效")
				}
			case "ip_is_private":
				if _, ok := v.(bool); !ok {
					return nil, errors.New("私网规则无效")
				}
			default:
				if !texts(v) {
					return nil, errors.New("路由匹配字段无效")
				}
			}
		}
	}
	if seen["probe-in"] {
		if len(rules) == 0 {
			return nil, errors.New("缺少探测入口规则")
		}
		first, _ := object(rules[0])
		b, _ := json.Marshal(first["inbound"])
		if string(b) != `["probe-in"]` || first["action"] != "route" || first["outbound"] != "proxy" {
			return nil, errors.New("探测入口必须直接验证所选代理")
		}
	}
	sets, e := array(route["rule_set"])
	if e != nil || len(sets) > 1 {
		return nil, errors.New("规则集合无效")
	}
	if len(sets) == 1 {
		rs, e := object(sets[0])
		if e != nil {
			return nil, e
		}
		if e = keys(rs, "type", "tag", "format", "path"); e != nil {
			return nil, e
		}
		if rs["type"] != "local" || rs["tag"] != "geoip-cn" || rs["format"] != "binary" {
			return nil, errors.New("仅支持本地 GeoIP 规则")
		}
		rs["path"] = filepath.ToSlash(geoPath)
	}
	dns, e := object(c["dns"])
	if e != nil {
		return nil, e
	}
	if e = keys(dns, "servers", "rules", "final", "strategy"); e != nil {
		return nil, e
	}
	if dns["final"] != "remote" || dns["strategy"] != "prefer_ipv4" {
		return nil, errors.New("DNS 参数无效")
	}
	servers, e := array(dns["servers"])
	if e != nil || len(servers) != 2 {
		return nil, errors.New("DNS 服务器无效")
	}
	for _, raw := range servers {
		m, e := object(raw)
		if e != nil {
			return nil, e
		}
		if e = keys(m, "type", "tag", "server", "server_port", "path", "detour"); e != nil {
			return nil, e
		}
		if m["type"] == "local" {
			if m["tag"] != "local" || len(m) != 2 {
				return nil, errors.New("本地 DNS 无效")
			}
		} else if m["type"] != "https" || m["tag"] != "remote" || m["server"] != "1.1.1.1" || m["server_port"] != float64(443) || m["path"] != "/dns-query" || m["detour"] != "proxy" {
			return nil, errors.New("远程 DNS 无效")
		}
	}
	dnsRules, e := array(dns["rules"])
	if e != nil {
		return nil, e
	}
	for _, raw := range dnsRules {
		m, e := object(raw)
		if e != nil {
			return nil, e
		}
		if e = keys(m, "domain", "server"); e != nil {
			return nil, e
		}
		if !texts(m["domain"]) || m["server"] != "local" {
			return nil, errors.New("DNS 匹配字段无效")
		}
	}
	exp, e := object(c["experimental"])
	if e != nil {
		return nil, e
	}
	if e = keys(exp, "clash_api"); e != nil {
		return nil, e
	}
	api, e := object(exp["clash_api"])
	if e != nil {
		return nil, e
	}
	if e = keys(api, "external_controller", "secret"); e != nil {
		return nil, e
	}
	host, p, e := net.SplitHostPort(text(api["external_controller"]))
	if e != nil || host != "127.0.0.1" || p == "0" || len(text(api["secret"])) < 32 {
		return nil, errors.New("控制接口必须仅监听本机并使用密钥")
	}
	return json.Marshal(c)
}

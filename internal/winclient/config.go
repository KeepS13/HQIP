package winclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	if e = validateRoutingConfig(c, seen, geoPath); e != nil {
		return nil, e
	}
	if e = validateDNSConfig(c); e != nil {
		return nil, e
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

package client

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"slices"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/rules"
)

const CoreSHA256 = "1fc5e70e367621aa95bcc70847f504a819f40f775a859ac95be98e83016c80a0"

type ConfigOptions struct {
	Profile   Profile
	Bundle    rules.Bundle
	Selected  string
	Mode      string
	TUN       bool
	MixedPort int
	ProbePort int
	APIPort   int
	StatsPort int
	APISecret string
	GeoPath   string
	ServerURL string
}

func GenerateConfig(o ConfigOptions) ([]byte, error) {
	if o.Mode != "rule" && o.Mode != "global" {
		return nil, errors.New("未知路由模式")
	}
	if o.MixedPort < 1 || o.MixedPort > 65535 {
		return nil, errors.New("代理端口无效")
	}
	cachedGeo := o.Bundle.GeoIPCN.CIDRs
	// Apply the shipped policy even when the server still serves the historical
	// LAN-only bundle. This changes generated configuration, never server hashes.
	if o.Mode == "rule" {
		var err error
		o.Bundle, err = rules.WithMainlandBypass(o.Bundle)
		if err != nil {
			return nil, err
		}
	}
	out := []map[string]any{{"type": "direct", "tag": "direct"}}
	tags := []string{}
	found := false
	domains := []string{}
	cidrs := []string{}
	u, _ := url.Parse(o.ServerURL)
	if migrateOfficialServerURL(o.ServerURL) == DefaultServerURL {
		domains = append(domains, officialBypassDomains()...)
	} else if u != nil && u.Hostname() != "" {
		domains = append(domains, u.Hostname())
	}
	for _, n := range o.Profile.Nodes {
		if n.ID == "" || n.Server == "" || n.SNI == "" || n.Password == "" || n.Port < 1 || n.Port > 65535 {
			return nil, errors.New("节点配置不完整")
		}
		if !n.Ready {
			continue
		}
		if n.ID == o.Selected {
			found = true
		}
		tag := "node-" + n.ID
		tags = append(tags, tag)
		domains = append(domains, n.Server)
		if ip := net.ParseIP(n.Server); ip != nil {
			suffix := "/128"
			if ip.To4() != nil {
				suffix = "/32"
			}
			cidrs = append(cidrs, n.Server+suffix)
		}
		transport := model.ClientNode{Protocol: n.Protocol, Server: n.Server, Port: n.Port, Password: n.Password, SNI: n.SNI, Reality: &n.Reality}
		outbound, err := transport.SingBoxOutbound(tag)
		if err != nil {
			return nil, err
		}
		out = append(out, outbound)
	}
	if !found {
		return nil, errors.New("目标节点尚未完成授权同步或已不可用")
	}
	out = append(out, map[string]any{"type": "selector", "tag": "proxy", "outbounds": tags, "default": "node-" + o.Selected, "interrupt_exist_connections": true})
	routeRules := []map[string]any{
		{"inbound": []string{"probe-in"}, "action": "route", "outbound": "proxy"},
		{"domain": domains, "action": "route", "outbound": "direct"},
	}
	if len(cidrs) > 0 {
		routeRules = append(routeRules, map[string]any{"ip_cidr": cidrs, "action": "route", "outbound": "direct"})
	}
	business := []string{"mixed-in"}
	if o.TUN {
		business = append(business, "tun-in")
		// Godot's controller lives in its own companion process. Keep its
		// management sockets outside TUN just like the original Wails host.
		routeRules = append(routeRules, map[string]any{"inbound": []string{"tun-in"}, "process_name": []string{"HQIP.exe", "hqip-windows-amd64.exe", "hqip-runtime.exe", "sing-box.exe", "hqip-helper.exe"}, "action": "route", "outbound": "direct"})
	}
	// Pause precedes DNS, private-address and user rules. Proxy traffic never
	// falls through to a direct outbound while authorization is stale.
	routeRules = append(routeRules,
		map[string]any{"clash_mode": suspendedClashMode, "inbound": business, "action": "reject"},
		map[string]any{"action": "sniff"},
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
		map[string]any{"ip_is_private": true, "action": "route", "outbound": "direct"},
	)
	if o.TUN {
		routeRules = append(routeRules, map[string]any{"inbound": []string{"tun-in"}, "process_name": []string{"ssh.exe"}, "action": "route", "outbound": "direct"})
	}
	if o.Mode == "rule" {
		rr, e := o.Bundle.RouteRules("proxy", "direct", "geoip-cn")
		if e != nil {
			return nil, e
		}
		for _, rule := range rr {
			// Resolve through DNS policy, not an unconditional local resolver.
			// Mainland/control domains use local DNS; unmatched domains use DoH
			// through the node before CN-IP classification. A polluted local
			// answer must never force an overseas domain onto the direct route.
			if _, processRule := rule["process_name"]; processRule {
				if !o.TUN {
					continue
				}
				rule["inbound"] = []string{"tun-in"}
			}
			routeRules = append(routeRules, rule)
		}
	}
	inbounds := []map[string]any{{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": o.MixedPort, "set_system_proxy": false}}
	if o.ProbePort > 0 {
		inbounds = append(inbounds, map[string]any{"type": "mixed", "tag": "probe-in", "listen": "127.0.0.1", "listen_port": o.ProbePort, "set_system_proxy": false})
	}
	if o.TUN {
		inbounds = append(inbounds, map[string]any{"type": "tun", "tag": "tun-in", "interface_name": "HQIP", "address": []string{"172.28.91.1/30", "fdfe:dcba:9876::1/126"}, "auto_route": true, "strict_route": true, "stack": "mixed"})
	}
	ruleSets := []map[string]any{}
	dnsRules := []map[string]any{{"domain": domains, "server": "local"}}
	if o.Mode == "rule" {
		// Reuse the compiled cache only when it represents the effective CIDRs.
		// An older server/cache must not undo the shipped mainland IP policy.
		if o.GeoPath != "" && slices.Equal(cachedGeo, o.Bundle.GeoIPCN.CIDRs) {
			ruleSets = append(ruleSets, map[string]any{"type": "local", "tag": "geoip-cn", "format": "binary", "path": filepath.ToSlash(o.GeoPath)})
		} else {
			ruleSets = append(ruleSets, map[string]any{"type": "inline", "tag": "geoip-cn", "rules": []map[string]any{{"ip_cidr": o.Bundle.GeoIPCN.CIDRs}}})
		}
		dnsRules = append(dnsRules, o.Bundle.DomainDNSRules("local", "remote")...)
	}
	config := map[string]any{
		"log":      map[string]any{"level": "error", "timestamp": true},
		"dns":      map[string]any{"servers": []map[string]any{{"type": "local", "tag": "local"}, {"type": "https", "tag": "remote", "server": "1.1.1.1", "server_port": 443, "path": "/dns-query", "detour": "proxy"}}, "rules": dnsRules, "final": "remote", "strategy": "prefer_ipv4"},
		"inbounds": inbounds, "outbounds": out,
		"route":        map[string]any{"auto_detect_interface": true, "default_domain_resolver": "local", "rules": routeRules, "final": "proxy", "rule_set": ruleSets},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": net.JoinHostPort("127.0.0.1", portString(o.APIPort)), "secret": o.APISecret}},
	}
	if o.StatsPort > 0 {
		config["experimental"].(map[string]any)["v2ray_api"] = map[string]any{"listen": net.JoinHostPort("127.0.0.1", portString(o.StatsPort)), "stats": map[string]any{"enabled": true, "outbounds": []string{"proxy"}}}
	}
	return json.MarshalIndent(config, "", "  ")
}

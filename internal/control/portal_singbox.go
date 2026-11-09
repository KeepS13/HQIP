package control

import (
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/rules"
)

var errPortalDefaultNodeUnavailable = errors.New("默认节点尚未就绪，请等待恢复或联系管理员调整默认节点")

// portalSingBoxSubscription produces a complete remote profile for the official
// Android and Apple clients. It targets the same sing-box 1.14.4 as HQIP, without
// desktop-specific file paths, process bypasses or a listening control API.
func portalSingBoxSubscription(profile model.ClientProfile, bundle rules.Bundle, serviceURLs ...string) ([]byte, error) {
	var err error
	bundle, err = rules.Normalize(bundle)
	if err != nil {
		return nil, err
	}
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	tags := []string{}
	domains, cidrs := []string{}, []string{}
	seenHosts, seenTags := map[string]bool{}, map[string]bool{}
	addBypass := func(host string) {
		if host == "" || seenHosts[host] {
			return
		}
		seenHosts[host] = true
		if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
			cidrs = append(cidrs, netip.PrefixFrom(ip, ip.BitLen()).String())
		} else {
			domains = append(domains, host)
		}
	}
	for _, raw := range serviceURLs {
		if u, parseErr := url.Parse(raw); parseErr == nil && (u.Scheme == "https" || u.Scheme == "http") {
			addBypass(u.Hostname())
		}
	}
	defaultTag := ""
	for _, node := range profile.Nodes {
		if !node.Ready {
			continue
		}
		if node.ID == "" || node.Server == "" || node.SNI == "" || node.Password == "" || node.Port < 1 || node.Port > 65535 {
			return nil, errors.New("invalid subscription node")
		}
		tag := node.Name + " · " + node.ID
		if seenTags[tag] {
			return nil, errors.New("duplicate subscription node")
		}
		seenTags[tag] = true
		addBypass(node.Server)
		if node.ID == profile.DefaultNodeID {
			defaultTag = tag
			tags = append([]string{tag}, tags...)
		} else {
			tags = append(tags, tag)
		}
		outbounds = append(outbounds, map[string]any{
			"type": "hysteria2", "tag": tag, "server": node.Server,
			"server_port": node.Port, "password": node.Password,
			"tls": map[string]any{"enabled": true, "server_name": node.SNI, "insecure": false},
		})
	}
	if defaultTag == "" {
		return nil, errPortalDefaultNodeUnavailable
	}
	outbounds = append(outbounds, map[string]any{
		"type": "selector", "tag": "proxy", "outbounds": tags,
		"default": defaultTag, "interrupt_exist_connections": true,
	})
	routeRules := []map[string]any{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}}
	dnsRules := []map[string]any{}
	if len(domains) > 0 {
		routeRules = append(routeRules, map[string]any{"domain": domains, "action": "route", "outbound": "direct"})
		dnsRules = append(dnsRules, map[string]any{"domain": domains, "server": "local"})
	}
	if len(cidrs) > 0 {
		routeRules = append(routeRules, map[string]any{"ip_cidr": cidrs, "action": "route", "outbound": "direct"})
	}
	translated, err := bundle.RouteRules("proxy", "direct", "geoip-cn")
	if err != nil {
		return nil, err
	}
	routeRules = append(routeRules, translated...)
	ruleSets := []map[string]any{}
	if len(bundle.GeoIPCN.CIDRs) > 0 {
		ruleSets = append(ruleSets, map[string]any{
			"type": "inline", "tag": "geoip-cn",
			"rules": []map[string]any{{"ip_cidr": bundle.GeoIPCN.CIDRs}},
		})
	}
	config := map[string]any{
		"log": map[string]any{"level": "warn", "timestamp": true},
		"dns": map[string]any{
			"servers": []map[string]any{
				{"type": "local", "tag": "local"},
				{"type": "https", "tag": "remote", "server": "1.1.1.1", "server_port": 443, "path": "/dns-query", "detour": "proxy"},
			},
			"rules": dnsRules, "final": "remote", "strategy": "prefer_ipv4",
		},
		"inbounds": []map[string]any{
			{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890, "set_system_proxy": false},
			{"type": "tun", "tag": "tun-in", "address": []string{"172.28.91.1/30", "fdfe:dcba:9876::1/126"}, "auto_route": true, "strict_route": true, "stack": "mixed"},
		},
		"outbounds": outbounds,
		"route": map[string]any{
			"auto_detect_interface": true, "default_domain_resolver": "local",
			"rules": routeRules, "final": "proxy", "rule_set": ruleSets,
		},
	}
	return json.MarshalIndent(config, "", "  ")
}

package winclient

import (
	"encoding/json"
	"testing"
)

func TestMainlandPrivilegedFieldsStayDeclarative(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"remote-resolve": func(c map[string]any) {
			c["route"].(map[string]any)["rules"] = []any{map[string]any{"action": "resolve", "server": "untrusted"}}
		},
		"server-on-route": func(c map[string]any) {
			c["route"].(map[string]any)["rules"] = []any{map[string]any{"action": "route", "server": "local"}}
		},
		"inline-file": func(c map[string]any) {
			c["route"].(map[string]any)["rule_set"] = []any{map[string]any{"type": "inline", "tag": "geoip-cn", "path": "C:/private", "rules": []any{map[string]any{"ip_cidr": []string{"1.0.1.0/24"}}}}}
		},
		"inline-action": func(c map[string]any) {
			c["route"].(map[string]any)["rule_set"] = []any{map[string]any{"type": "inline", "tag": "geoip-cn", "rules": []any{map[string]any{"action": "route", "ip_cidr": []string{"1.0.1.0/24"}}}}}
		},
		"inline-invalid-ip": func(c map[string]any) {
			c["route"].(map[string]any)["rule_set"] = []any{map[string]any{"type": "inline", "tag": "geoip-cn", "rules": []any{map[string]any{"ip_cidr": []string{"not-a-prefix"}}}}}
		},
		"dns-unknown-server": func(c map[string]any) {
			c["dns"].(map[string]any)["rules"] = []any{map[string]any{"domain_suffix": []string{"cn"}, "server": "untrusted"}}
		},
		"dns-file": func(c map[string]any) {
			c["dns"].(map[string]any)["rules"] = []any{map[string]any{"path": "C:/private", "server": "local"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := sampleTUN(t)
			mutate(config)
			raw, _ := json.Marshal(config)
			if _, err := validateTUN(raw, "safe.srs"); err == nil {
				t.Fatal("unsafe privileged field accepted")
			}
		})
	}
}

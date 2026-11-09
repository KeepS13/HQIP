package winclient

import (
	"encoding/json"
	"testing"
)

func TestHelperPauseRuleIsStrict(t *testing.T) {
	for _, kind := range []string{"valid", "direct", "wrong-mode", "probe", "missing-inbound", "extra-field"} {
		t.Run(kind, func(t *testing.T) {
			c := sampleTUN(t)
			rule := map[string]any{"clash_mode": "hqip-paused", "inbound": []string{"mixed-in", "tun-in"}, "action": "reject"}
			switch kind {
			case "direct":
				rule["action"] = "route"
				rule["outbound"] = "direct"
			case "wrong-mode":
				rule["clash_mode"] = "Global"
			case "probe":
				rule["inbound"] = []string{"probe-in"}
			case "missing-inbound":
				delete(rule, "inbound")
			case "extra-field":
				rule["domain"] = []string{"example.com"}
			}
			route := c["route"].(map[string]any)
			route["rules"] = append(route["rules"].([]any), rule)
			data, _ := json.Marshal(c)
			_, err := validateTUN(data, "geoip-cn.srs")
			if (err == nil) != (kind == "valid") {
				t.Fatalf("kind %s: %v", kind, err)
			}
		})
	}
}

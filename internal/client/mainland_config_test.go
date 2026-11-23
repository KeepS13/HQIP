package client

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
)

func TestMainlandConfigUpgradesOldBundleAndHonorsGlobal(t *testing.T) {
	legacy := rules.Bundle{SchemaVersion: 1, Rules: []rules.Rule{{Type: "MATCH", Outbound: "proxy"}}}
	for _, mode := range []string{"rule", "global"} {
		o := ConfigOptions{
			Profile: Profile{Nodes: []Node{{ID: "1", Server: "node.example.com", SNI: "node.example.com", Port: 443, Password: "test-password", Ready: true}}},
			Bundle:  legacy, Selected: "1", Mode: mode, MixedPort: 14001, APIPort: 14002,
			APISecret: "test-secret", ServerURL: "https://example.com", GeoPath: "stale-server-geoip.srs",
		}
		body, err := GenerateConfig(o)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Route struct {
				Rules   []json.RawMessage
				RuleSet []json.RawMessage `json:"rule_set"`
			}
			DNS struct{ Rules []json.RawMessage }
		}
		if err = json.Unmarshal(body, &config); err != nil {
			t.Fatal(err)
		}
		if mode == "global" {
			if bytes.Contains(body, []byte("bilibili.com")) || len(config.Route.RuleSet) != 0 {
				t.Fatal("mainland bypass leaked into explicit global mode")
			}
			continue
		}
		if len(config.Route.RuleSet) != 1 || bytes.Contains(body, []byte(o.GeoPath)) || !bytes.Contains(config.Route.RuleSet[0], []byte(`"inline"`)) {
			t.Fatal("old GeoIP cache overrode the effective mainland snapshot")
		}
		found := false
		for _, row := range config.DNS.Rules {
			if bytes.Contains(row, []byte("bilibili.com")) && bytes.Contains(row, []byte(`"local"`)) {
				found = true
			}
		}
		if !found {
			t.Fatal("mainland domain DNS still uses the proxy")
		}
	}
}

package client

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
)

func TestProcessBypassRulesOnlyAppearWithTUNInbound(t *testing.T) {
	profile := Profile{Nodes: []Node{{ID: "1", Server: "node.example.com", SNI: "node.example.com", Port: 443, Password: "random-password-long-enough", Ready: true}}}
	bundle := rules.Default()
	last := bundle.Rules[len(bundle.Rules)-1]
	bundle.Rules = append(bundle.Rules[:len(bundle.Rules)-1], rules.Rule{Type: "PROCESS-NAME", Value: "acceptance.exe", Outbound: "direct"}, last)
	for _, tun := range []bool{false, true} {
		t.Run(map[bool]string{false: "system-proxy", true: "tun"}[tun], func(t *testing.T) {
			data, err := GenerateConfig(ConfigOptions{
				Profile: profile, Bundle: bundle, Selected: "1", Mode: "rule", TUN: tun,
				MixedPort: 14001, ProbePort: 14003, APIPort: 14002,
				APISecret: "01234567890123456789012345678901", ServerURL: "https://example.com",
			})
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Route struct {
					Rules []map[string]any `json:"rules"`
				} `json:"route"`
			}
			if err = json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			processRules := 0
			for _, rule := range config.Route.Rules {
				if _, hasProcess := rule["process_name"]; !hasProcess {
					continue
				}
				processRules++
				if !tun {
					t.Fatal("process bypass rule must not be present without a TUN inbound")
				}
				inbounds, ok := rule["inbound"].([]any)
				if !ok || len(inbounds) != 1 || inbounds[0] != "tun-in" {
					t.Fatalf("TUN process bypass is not scoped to the TUN inbound: %#v", rule["inbound"])
				}
			}
			if want := map[bool]int{false: 0, true: 3}[tun]; processRules != want {
				t.Fatalf("process rules = %d, want %d", processRules, want)
			}
		})
	}
}

package client

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/rules"
)

func TestVisionProfileRefreshAndCoreConfiguration(t *testing.T) {
	legacy := Node{ID: "node:vless", Protocol: "vless", Server: "node.example.com", SNI: model.DefaultRealityServerName, Port: 443, Ready: true, Password: model.RealityUUID(strings.Repeat("a", 43)), Reality: model.RealityEndpoint{Port: 443, ServerName: model.DefaultRealityServerName, PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortID: "0123456789abcdef"}}
	vision := legacy
	vision.Reality.Flow = model.RealityVisionFlow
	old, next := Profile{Nodes: []Node{legacy}}, Profile{Nodes: []Node{vision}}
	if !connectionConfigurationChanged(old, next, legacy.ID) || !connectionConfigurationChanged(next, old, legacy.ID) {
		t.Fatal("upgrade/rollback must replace a connected core configuration")
	}
	for _, node := range []Node{legacy, vision} {
		data, err := GenerateConfig(ConfigOptions{Profile: Profile{Nodes: []Node{node}}, Bundle: rules.Default(), Selected: node.ID, Mode: "rule", MixedPort: 14001, ProbePort: 14003, APIPort: 14002, ServerURL: DefaultServerURL})
		if err != nil {
			t.Fatal(err)
		}
		var config struct{ Outbounds []map[string]any }
		if err = json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, out := range config.Outbounds {
			if out["type"] != "vless" {
				continue
			}
			found = true
			flow, _ := out["flow"].(string)
			if flow != node.Reality.Flow || out["server"] != node.Server || out["packet_encoding"] != "xudp" {
				t.Fatal("core transport differs from reported endpoint")
			}
		}
		if !found {
			t.Fatal("VLESS outbound missing")
		}
	}
}

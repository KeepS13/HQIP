package model

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRealityFlowValidationAndLegacyExports(t *testing.T) {
	endpoint := RealityEndpoint{Port: 443, ServerName: DefaultRealityServerName, PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortID: "0123456789abcdef"}
	node := ClientNode{Protocol: "vless", Server: "node.example.com", Port: 443, SNI: endpoint.ServerName, Password: RealityUUID(strings.Repeat("a", 43)), Reality: &endpoint}
	for _, flow := range []string{"", RealityVisionFlow, "unsupported-flow"} {
		endpoint.Flow = flow
		sing, singErr := node.SingBoxOutbound("proxy")
		clash, clashErr := node.ClashProxy("node")
		if flow == "unsupported-flow" {
			if singErr == nil || clashErr == nil || endpoint.Validate() == nil {
				t.Fatal("unknown flow was accepted")
			}
			continue
		}
		if singErr != nil || clashErr != nil {
			t.Fatal(singErr, clashErr)
		}
		for _, exported := range []map[string]any{sing, clash} {
			got, present := exported["flow"]
			if (flow == "" && present) || (flow != "" && got != flow) {
				t.Fatal("wrong flow for node capability", exported)
			}
		}
	}
}

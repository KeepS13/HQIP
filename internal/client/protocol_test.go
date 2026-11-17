package client

import (
	"testing"
	"time"
)

func TestProtocolChoicesKeepPhysicalAuthorization(t *testing.T) {
	profile := Profile{SubscriptionID: "a", Subscriptions: []Subscription{{ID: "a", Status: "active", ExpiresAt: time.Now().Add(time.Hour), NodeIDs: []string{"node"}}}, AvailableNodes: []Node{{ID: "node", Ready: true}, {ID: "node:vless", NodeID: "node", Protocol: "vless", Ready: true}, {ID: "unassigned:vless", NodeID: "unassigned", Protocol: "vless", Ready: true}}}
	choices, nodes := nodeChoices(profile)
	if len(choices) != 2 || len(nodes) != 2 || choices[1].NodeID != "node:vless" || choices[1].SubscriptionID != "a" {
		t.Fatal("protocol expansion changed authorization", choices)
	}
	if len(profile.Subscriptions[0].NodeIDs) != 1 {
		t.Fatal("presentation mutated physical allocation")
	}
	profile.Nodes = profile.AvailableNodes
	if !idleProtocol(profile, "node:vless") || idleProtocol(profile, "node") || idleProtocol(profile, "unknown") {
		t.Fatal("wrong lease lifetime policy")
	}
	if nodeProbeKey(Node{ID: "a", Server: "203.0.113.1", Port: 443, SNI: "x.test"}) == nodeProbeKey(Node{ID: "b", Protocol: "vless", Server: "203.0.113.1", Port: 443, SNI: "x.test"}) {
		t.Fatal("TCP/UDP measurements were deduplicated")
	}
}

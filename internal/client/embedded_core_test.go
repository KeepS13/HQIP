package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
)

// A rejected native start lets us inspect the platform contract without opening
// a VPN, connecting a node, or changing the developer machine's network.
type rejectedEmbeddedHost struct {
	config string
	stops  int
}

func (h *rejectedEmbeddedHost) Start(config string) error {
	h.config = config
	return errors.New("test native start failure")
}
func (h *rejectedEmbeddedHost) Stop() error { h.stops++; return nil }

func TestEmbeddedVPNUsesPortableTCPStackAndCleansFailedStart(t *testing.T) {
	host := &rejectedEmbeddedHost{}
	core := NewEmbeddedCore(t.TempDir(), fakePlatform{}, host)
	profile := Profile{Nodes: []Node{{ID: "node", Server: "node.example", SNI: "node.example", Port: 443, Password: "test-only-password", Ready: true}}}
	if err := core.Apply(context.Background(), profile, rules.Default(), "node", "rule", false, "https://account.example"); err == nil || host.config != "" {
		t.Fatal("must reject unsupported proxy mode before native start")
	}
	if err := core.Apply(context.Background(), profile, rules.Default(), "node", "rule", true, "https://account.example"); err == nil {
		t.Fatal("native start failure must propagate")
	}
	if host.stops != 1 {
		t.Fatalf("failed start cleanup calls = %d, want 1", host.stops)
	}
	var config struct {
		Inbounds []map[string]any `json:"inbounds"`
		Route    struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(host.config), &config); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, inbound := range config.Inbounds {
		if inbound["type"] == "tun" {
			found = true
			if inbound["stack"] != "gvisor" || inbound["mtu"] != float64(1500) {
				t.Fatal("Android VPN must not depend on desktop kernel TCP routes or jumbo MTU")
			}
		}
	}
	if !found {
		t.Fatal("missing VPN inbound")
	}
	for _, rule := range config.Route.Rules {
		if _, desktopRule := rule["process_name"]; desktopRule {
			t.Fatal("desktop process rules must not escape to Android")
		}
	}
}

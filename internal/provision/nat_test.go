package provision

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNATPreflightRequiresVerifiedLocalSource(t *testing.T) {
	r := New(Options{ControllerID: "controller-one"})
	n := validNode()
	inv := inventoryFixture()
	inv.BoundIPs = []string{"127.0.0.1", "10.0.186.2"}
	inv.RouteIP, inv.ExitIP = "10.0.186.2", n.PublicIP
	pre := r.assess(n, inv)
	if !pre.CanInstall || pre.BindIP != inv.RouteIP {
		t.Fatalf("verified NAT rejected: %+v", pre)
	}
	for _, change := range []func(*hostInventory){
		func(i *hostInventory) { i.ExitIP = "" },
		func(i *hostInventory) { i.ExitIP = "203.0.113.99" },
		func(i *hostInventory) { i.RouteIP = "10.0.186.3" },
		func(i *hostInventory) { i.RouteIP = "127.0.0.1" },
		func(i *hostInventory) { i.RouteIP = "0.0.0.0" },
	} {
		copy := inv
		change(&copy)
		if result := r.assess(n, copy); result.CanInstall || result.CanTakeover {
			t.Fatalf("unverified mapping accepted: %+v", copy)
		}
	}
	inv.Owner = &instanceOwner{NodeID: n.ID, ControllerID: "controller-one", MachineID: inv.MachineID, PublicIP: n.PublicIP, InstallationVersion: installationVersion}
	if result := r.assess(n, inv); !result.CanInstall || !result.ExistingManaged {
		t.Fatalf("NAT redeployment lost public ownership: %+v", result)
	}
	inv.Owner.PublicIP = "203.0.113.99"
	if r.assess(n, inv).CanInstall {
		t.Fatal("NAT bypassed owner identity")
	}
}

func TestNATPreflightChecksLocalPortConflicts(t *testing.T) {
	r := New(Options{ControllerID: "controller-one"})
	n := validNode()
	inv := inventoryFixture()
	inv.BoundIPs = []string{"10.0.186.2"}
	inv.RouteIP, inv.ExitIP = "10.0.186.2", n.PublicIP
	for _, socket := range []hostSocket{
		{Protocol: "udp", Host: "10.0.186.2", Port: n.Port},
		{Protocol: "udp", Host: "0.0.0.0", Port: n.Port},
		{Protocol: "tcp", Host: "10.0.186.2", Port: 80},
	} {
		inv.Sockets = []hostSocket{socket}
		if r.assess(n, inv).CanInstall {
			t.Fatal("local conflict ignored", socket)
		}
	}
	inv.Sockets = []hostSocket{{Protocol: "udp", Host: "10.0.186.3", Port: n.Port}}
	if !r.assess(n, inv).CanInstall {
		t.Fatal("another interface blocked installation")
	}
}

func TestNATConfigAndFirewallUseLocalAddress(t *testing.T) {
	n := validNode()
	const bindIP = "10.0.186.2"
	data, err := instanceConfig(n, bindIP, false, "stats-secret")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["listen"] != bindIP+":443" || cfg["acme"].(map[string]any)["listenHost"] != bindIP {
		t.Fatal("NAT listener still uses advertised public address", string(data))
	}
	direct := cfg["outbounds"].([]any)[0].(map[string]any)["direct"].(map[string]any)
	if direct["bindIPv4"] != bindIP || n.PublicIP != "203.0.113.2" {
		t.Fatal("local egress or public node identity changed")
	}
	for _, rule := range nodeFirewallRules(n, bindIP, false, 443) {
		if rule.IP != bindIP {
			t.Fatal("firewall would match pre-NAT destination", rule)
		}
	}
	for _, invalid := range []string{"", "0.0.0.0", "127.0.0.1", "::1", "224.0.0.1"} {
		if _, err := instanceConfig(n, invalid, false, "secret"); err == nil {
			t.Fatal("invalid bind accepted", invalid)
		}
	}
}

func TestNATAgentReleaseKeepsNetworkCapability(t *testing.T) {
	release := AgentRelease{NodeNetworkVersion: 1, SourceSHA: strings.Repeat("a", 40)}
	var manifest map[string]any
	if err := json.Unmarshal(release.upgradeManifest("amd64", "hash"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["nodeNetworkVersion"] != float64(1) {
		t.Fatal("Agent upgrade lost NAT capability")
	}
}

package provision

import (
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
)

func TestPerIPConfigBindsIngressAndEgressAndSeparateSecrets(t *testing.T) {
	for _, n := range []model.Node{
		{ID: "node-a", PublicIP: "203.0.113.1", Domain: "a.example.com", Port: 443, AuthPort: 20000, StatsPort: 20001},
		{ID: "node-b", PublicIP: "203.0.113.2", Domain: "b.example.com", Port: 443, AuthPort: 20002, StatsPort: 20003},
	} {
		data, err := serverConfig(n, false, n.ID+"-secret")
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err = yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg["listen"] != n.PublicIP+":443" {
			t.Fatal("wildcard ingress can leak another IP", cfg["listen"])
		}
		direct := cfg["outbounds"].([]any)[0].(map[string]any)["direct"].(map[string]any)
		if direct["bindIPv4"] != n.PublicIP || direct["mode"] != "4" {
			t.Fatal("outbound source not fixed to node IP")
		}
		acme := cfg["acme"].(map[string]any)
		if acme["listenHost"] != n.PublicIP {
			t.Fatal("HTTP validation must not capture other IPs")
		}
		if !strings.Contains(acme["dir"].(string), n.ID) {
			t.Fatal("shared certificate store")
		}
		if cfg["trafficStats"].(map[string]any)["secret"] != n.ID+"-secret" {
			t.Fatal("stats secret changed")
		}
	}
}

func TestManagedCertificateDoesNotClaimTCP80OrSharedCertificate(t *testing.T) {
	n := validNode()
	n.CertificateMode = "cloudflare"
	data, err := serverConfig(n, true, "secret")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["acme"]; ok {
		t.Fatal("managed certificate accidentally enabled HTTP validation")
	}
	tls := cfg["tls"].(map[string]any)
	if tls["cert"] != "/var/lib/hqip-nodes/node-1/certificates/current/cert.pem" || tls["key"] != "/var/lib/hqip-nodes/node-1/certificates/current/key.pem" {
		t.Fatal("certificate path escaped node")
	}
	control, caddy := deploymentTopology(n, "same", "same", "linux")
	if !control || caddy {
		t.Fatal("Cloudflare certificate should ignore Caddy legacy hint")
	}
}

func inventoryFixture() hostInventory {
	return hostInventory{ServerInspection: ServerInspection{MachineID: "machine-one", Platform: "ubuntu 24.04", Architecture: "x86_64", BoundIPs: []string{"203.0.113.2", "203.0.113.3"}}, FreeBytes: 1 << 30, Sockets: []hostSocket{}, Units: []TakeoverCandidate{}}
}

func TestPreflightProtectsOtherInstancesAndRequiresExplicitLegacyTakeover(t *testing.T) {
	r := New(Options{ControllerID: "controller-one"})
	n := validNode()
	n.AuthPort = 20000
	n.StatsPort = 20001
	inv := inventoryFixture()
	inv.Sockets = []hostSocket{{Protocol: "udp", Host: "203.0.113.3", Port: 443, Units: []string{"hqip-hysteria-other.service"}}}
	if result := r.assess(n, inv); !result.CanInstall {
		t.Fatalf("unrelated IP blocked: %+v", result)
	}
	inv.Sockets[0].Host = "0.0.0.0"
	if result := r.assess(n, inv); result.CanInstall || result.CanTakeover {
		t.Fatal("wildcard other managed instance can be overwritten")
	}
	inv.Sockets[0].Units = []string{"hqip-hysteria.service"}
	inv.LegacyOwner = "old-node"
	inv.Units = []TakeoverCandidate{{Unit: "hqip-hysteria.service", Kind: "hqip-legacy", UnitHash: "original"}}
	result := r.assess(n, inv)
	if result.CanInstall || !result.CanTakeover || len(result.TakeoverCandidates) != 1 {
		t.Fatalf("legacy installation not offered explicitly: %+v", result)
	}
	inv.Sockets = append(inv.Sockets, hostSocket{Protocol: "tcp", Host: "0.0.0.0", Port: 80, Units: []string{"nginx.service"}})
	if r.assess(n, inv).CanTakeover {
		t.Fatal("takeover would claim unrelated HTTP website")
	}
	n.CertificateMode = "cloudflare"
	if !r.assess(n, inv).CanTakeover {
		t.Fatal("DNS-01 should permit unrelated TCP80 service")
	}
}

func TestPreflightIdentityAndSnapshotIgnoreFreeSpaceOnly(t *testing.T) {
	r := New(Options{ControllerID: "controller-one"})
	n := validNode()
	inv := inventoryFixture()
	inv.Owner = &instanceOwner{NodeID: n.ID, ControllerID: "controller-one", MachineID: inv.MachineID, PublicIP: n.PublicIP, InstallationVersion: 2}
	first := r.assess(n, inv)
	if !first.ExistingManaged || !first.CanInstall {
		t.Fatal("same node retry rejected")
	}
	inv.FreeBytes -= 4096
	if r.assess(n, inv).Fingerprint != first.Fingerprint {
		t.Fatal("unrelated disk writes invalidate ownership snapshot")
	}
	inv.Owner.ControllerID = "another-controller"
	result := r.assess(n, inv)
	if result.Fingerprint == first.Fingerprint || result.ExistingManaged || result.CanInstall {
		t.Fatal("foreign controller owner accepted")
	}
	inv.Owner = nil
	inv.BoundIPs = []string{"203.0.113.3"}
	if r.assess(n, inv).CanInstall {
		t.Fatal("unconfigured IP accepted")
	}
}

func TestUnitNamesAndContentAddressedBinariesAreIsolated(t *testing.T) {
	a := validNode()
	b := a
	b.ID = "other-node"
	b.PublicIP = "203.0.113.3"
	agentPath := contentPath("agent", []byte("agent"))
	corePath := contentPath("hysteria", []byte("core"))
	aAgent, aCore := instanceUnits(a, agentPath, corePath)
	bAgent, bCore := instanceUnits(b, agentPath, corePath)
	for _, content := range []string{aAgent, aCore} {
		if strings.Contains(content, "other-node") || strings.Contains(content, "/etc/hqip-node/") {
			t.Fatal("shared old config reused")
		}
	}
	for _, content := range []string{bAgent, bCore} {
		if strings.Contains(content, "node-1") || strings.Contains(content, "hqip-agent.service") {
			t.Fatal("other instance hard-coded")
		}
	}
	if contentPath("agent", []byte("changed")) == agentPath {
		t.Fatal("binary changed without changing immutable path")
	}
}

func TestMissingV2OwnerCannotProveMigratedLegacyNodeStopped(t *testing.T) {
	n := validNode()
	n.Managed = true
	n.InstallationVersion = 1
	inv := inventoryFixture()
	if confirmMissingInstance(n, inv) == nil {
		t.Fatal("missing v2 owner claimed migrated node safely stopped")
	}
	n.Managed = false
	inv.LegacyOwner = n.ID
	if confirmMissingInstance(n, inv) == nil {
		t.Fatal("matching old owner ignored")
	}
	inv.LegacyOwner = "other-node"
	if err := confirmMissingInstance(n, inv); err != nil {
		t.Fatal("failed unowned record should leave foreign legacy service unchanged", err)
	}
	inv.Units = []TakeoverCandidate{{Unit: pathsFor(n.ID).CoreUnit}}
	if confirmMissingInstance(n, inv) == nil {
		t.Fatal("own service without owner treated as absent")
	}
}

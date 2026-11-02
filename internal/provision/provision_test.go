package provision

import (
	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
	"testing"
)

func validNode() model.Node {
	return model.Node{ID: "node-1", Name: "Tokyo", SSHHost: "203.0.113.2", SSHUser: "root", SSHPort: 22, PublicIP: "203.0.113.2", Domain: "node.example.com", Port: 443}
}

func TestRejectUnsafeAddressesAndPorts(t *testing.T) {
	for _, edit := range []func(*model.Node){
		func(n *model.Node) { n.Domain = "example.com; echo test" },
		func(n *model.Node) { n.Domain = "https://example.com" },
		func(n *model.Node) { n.SSHUser = "root;id" },
		func(n *model.Node) { n.SSHHost = "host\nwhoami" },
		func(n *model.Node) { n.PublicIP = "127.0.0.1" },
		func(n *model.Node) { n.PublicIP = "169.254.169.254" },
		func(n *model.Node) { n.Port = 65536 },
	} {
		n := validNode()
		edit(&n)
		if Validate(n) == nil {
			t.Fatalf("accepted unsafe node: %+v", n)
		}
	}
	if err := Validate(validNode()); err != nil {
		t.Fatal(err)
	}
}

func TestSharedHostSeparatesACMEAndDoesNotApplyQuotas(t *testing.T) {
	n := validNode()
	bw := float64(1)
	quota := int64(1024)
	n.BandwidthMbps = &bw
	n.MonthlyQuotaBytes = &quota
	data, err := serverConfig(n, true, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if _, exists := config["bandwidth"]; exists {
		t.Fatal("informational capacity must not throttle clients")
	}
	acme := config["acme"].(map[string]any)
	if acme["listenHost"] != "127.0.0.1" || acme["http"].(map[string]any)["altPort"] != 18080 {
		t.Fatal("shared host ACME would conflict with website")
	}
	stats := config["trafficStats"].(map[string]any)
	if stats["listen"] != "127.0.0.1:18082" || stats["secret"] != "test-secret" {
		t.Fatal("stats must be private and authenticated")
	}
	if len(config["acl"].(map[string]any)["inline"].([]any)) < 10 {
		t.Fatal("private/metadata destinations are not protected")
	}
}

func TestShellQuoteTreatsPasswordAsData(t *testing.T) {
	if got := shellQuote("a'$(touch /tmp/test)\nb"); got != "'a'\"'\"'$(touch /tmp/test)\nb'" {
		t.Fatalf("invalid quoting: %q", got)
	}
}

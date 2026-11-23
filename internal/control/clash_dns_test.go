package control

import (
	"reflect"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
)

func TestClashPrivateDNSRetainsLANResolver(t *testing.T) {
	config := clashDNS(rules.Default())
	policy := config["nameserver-policy"].(map[string]any)
	for _, suffix := range []string{"localhost", "local", "lan"} {
		if !reflect.DeepEqual(policy["+."+suffix], []string{"system"}) {
			t.Error("private name sent to public DNS:", suffix)
		}
	}
	if !reflect.DeepEqual(policy["+.bilibili.com"], []string{"223.5.5.5", "119.29.29.29"}) {
		t.Fatal("mainland DNS is no longer local to China")
	}
}

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNATConfigPreservesPublicIdentityAndLocalBinding(t *testing.T) {
	cfg := model.AgentConfig{
		ControlURL: "https://control.example.com", NodeID: "nat-node", Token: strings.Repeat("a", 32),
		StatsSecret: strings.Repeat("b", 32), PublicIP: "45.205.28.141", BindIP: "10.0.186.2",
		Domain: "nat.example.com", Reality: &model.AgentRealityConfig{Port: 443, BackendPort: 443, ServerName: model.DefaultRealityServerName},
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.LocalIP() != "10.0.186.2" || loaded.PublicIP != cfg.PublicIP {
		t.Fatal("NAT configuration lost its address boundary", err)
	}
	loaded.BindIP = ""
	if loaded.LocalIP() != cfg.PublicIP {
		t.Fatal("legacy direct-IP behavior changed")
	}
	for _, invalid := range []string{"0.0.0.0", "::1", "224.0.0.1", "169.254.169.254", "example.com"} {
		copy := cfg
		copy.BindIP = invalid
		if normalizeConfig(&copy) == nil {
			t.Fatal("invalid local source accepted", invalid)
		}
	}
}

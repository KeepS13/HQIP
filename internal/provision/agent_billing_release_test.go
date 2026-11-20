package provision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedAgentUpgradePreservesBillingCapability(t *testing.T) {
	for _, version := range []int{0, 1} {
		directory := t.TempDir()
		source := strings.Repeat("a", 40)
		manifest, _ := json.Marshal(map[string]any{"sourceSHA": source, "trafficBillingVersion": version, "offlineAdmissionVersion": 1, "vlessRealityVersion": 1})
		if err := os.WriteFile(filepath.Join(directory, "manifest.json"), manifest, 0600); err != nil {
			t.Fatal(err)
		}
		runner := &Runner{options: Options{ArtifactDir: directory}}
		release, err := runner.AgentRelease()
		if err != nil {
			t.Fatal(err)
		}
		var delivered struct {
			TrafficBillingVersion int               `json:"trafficBillingVersion"`
			AgentSourceSHA        string            `json:"agentSourceSHA"`
			SHA256                map[string]string `json:"sha256"`
		}
		hash := strings.Repeat("b", 64)
		if err = json.Unmarshal(release.upgradeManifest("amd64", hash), &delivered); err != nil {
			t.Fatal(err)
		}
		if delivered.TrafficBillingVersion != version || delivered.AgentSourceSHA != source || delivered.SHA256["agent-linux-amd64"] != hash {
			t.Fatalf("managed upgrade lost release identity or billing capability: %+v", delivered)
		}
	}
}

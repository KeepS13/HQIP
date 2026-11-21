package provision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentReleasePreservesConnectionSlotsCapability(t *testing.T) {
	for _, version := range []int{0, 1} {
		dir := t.TempDir()
		manifest, _ := json.Marshal(map[string]any{"sourceSHA": strings.Repeat("a", 40), "nodeQuotaVersion": 1, "connectionSlotsVersion": version})
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0600); err != nil {
			t.Fatal(err)
		}
		r := &Runner{options: Options{ArtifactDir: dir}}
		release, err := r.AgentRelease()
		if err != nil {
			t.Fatal(err)
		}
		var delivered map[string]any
		if err = json.Unmarshal(release.upgradeManifest("amd64", strings.Repeat("b", 64)), &delivered); err != nil {
			t.Fatal(err)
		}
		if release.ConnectionSlotsVersion != version || delivered["connectionSlotsVersion"] != float64(version) {
			t.Fatal("managed upgrade dropped slot compatibility", delivered)
		}
	}
}

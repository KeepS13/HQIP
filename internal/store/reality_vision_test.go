package store

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestRealityVisionCapabilityUpgradeAndRollback(t *testing.T) {
	s, _ := fixture(t)
	node := newNode(t, s, "vision-report")
	endpoint := realityFixture(t, s, node.ID)
	for _, vision := range []bool{false, true, false} {
		report := model.AgentReport{Reality: &endpoint, Capabilities: []string{model.RealityCapability}}
		want := ""
		if vision {
			report.Capabilities = append(report.Capabilities, model.RealityVisionCapability)
			want = model.RealityVisionFlow
		}
		tx, err := s.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = recordReality(tx, node.ID, report, true); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		got, err := s.RealityEndpoint(node.ID)
		if err != nil || got == nil || got.Flow != want {
			t.Fatalf("vision=%v endpoint=%+v error=%v", vision, got, err)
		}
		if endpoint.Flow != "" {
			t.Fatal("report was mutated; old controllers would reject the extra flow field")
		}
		// The old endpoint schema can still consume reports from upgraded Agents.
		raw, _ := json.Marshal(report.Reality)
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		if _, present := wire["flow"]; present {
			t.Fatal("derived flow leaked into Agent report")
		}
		profile, err := s.expandProtocolNodes([]model.ClientNode{{ID: node.ID, Server: node.Domain}})
		if err != nil || len(profile) != 2 || profile[1].Reality.Flow != want {
			t.Fatal("capability transition did not reach profile", err)
		}
	}
}

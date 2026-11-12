package store

import (
	"errors"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestPortalPlanRequiresUpgradedNodeButAllowsOfflineEditing(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "legacy-plan-node")
	p := model.SubscriptionTemplate{ValidityDays: 1, Name: "restricted", Enabled: true, Listed: true, NodeIDs: []string{n.ID}, DefaultNodeID: n.ID, MaxConnections: 1}
	if _, err := s.SaveSubscriptionTemplate("", p, 0); err == nil {
		t.Fatal("plan accepted node without admission-v1")
	}
	if _, err := s.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]',last_seen=0,core_healthy=0 WHERE id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.SaveSubscriptionTemplate("", p, 0)
	if err != nil {
		t.Fatal("upgraded offline node prevented plan save", err)
	}
	p.Name = "edited while offline"
	if _, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision); err != nil {
		t.Fatal("upgraded offline node prevented plan edit", err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET lifecycle='retired' WHERE id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveSubscriptionTemplate("", p, 0); err == nil {
		t.Fatal("plan accepted retired upgraded node")
	}
}

func TestPortalEnabledCASDoesNotOverwriteNewerAssignment(t *testing.T) {
	s, _, u, plan, _, _ := portalFixture(t, 1, nil)
	before, err := s.PortalUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignSubscription(u.ID, plan.ID, "another-assignment", before.Revision, plan.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPortalEnabled(u.ID, false, before.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale disable overwrote newer user state", err)
	}
	current, err := s.PortalUser(u.ID)
	if err != nil || !current.Enabled {
		t.Fatal("CAS conflict mutated user", err)
	}
	if err = s.SetPortalEnabled(u.ID, false, current.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPortalEnabled(u.ID, true, current.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale enable undid newer suspension", err)
	}
}

func TestPortalProfileRetainsUnavailableDefaultWithoutFallback(t *testing.T) {
	s, _, u, p, primary, _ := portalFixture(t, 1, nil)
	other := newNode(t, s, "other-plan-node")
	if _, err := s.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]',last_seen=?,core_healthy=1 WHERE id=?", s.Now().Unix(), other.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(other.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	p.NodeIDs = append(p.NodeIDs, other.ID)
	if _, err := s.SaveSubscriptionTemplate(p.ID, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE nodes SET lifecycle='retired' WHERE id=?", primary.ID); err != nil {
		t.Fatal(err)
	}
	profile, _, err := s.PortalClientProfile(u.ID, "subscription", u.Subscriptions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.DefaultNodeID != other.ID || len(profile.Nodes) != 1 || profile.Nodes[0].ID != other.ID {
		t.Fatal("profile did not select the eligible replacement")
	}
}

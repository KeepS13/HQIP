package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodeDeletionBlocksAllTemplateReferencesWithoutMutation(t *testing.T) {
	s, _ := fixture(t)
	_, a, b := serverPair(t, s)
	if _, err := s.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]',managed=1`); err != nil {
		t.Fatal(err)
	}
	active, err := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "默认节点订阅", Enabled: true, Listed: true, ValidityDays: 30, MaxConnections: 1, NodeIDs: []string{a.ID, b.ID}, DefaultNodeID: a.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "隐藏停用订阅", Enabled: false, Listed: false, ValidityDays: 30, MaxConnections: 1, NodeIDs: []string{a.ID, b.ID}, DefaultNodeID: b.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.NodeSubscriptionReferences(a.ID)
	if err != nil || len(refs) != 2 {
		t.Fatal(refs, err)
	}
	byID := map[string]NodeSubscriptionReference{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	if byID[active.ID].DefaultNode || byID[disabled.ID].DefaultNode || byID[disabled.ID].Enabled || byID[disabled.ID].Listed {
		t.Fatal("reference roles or disabled template omitted", byID)
	}
	var beforeJobs string
	_ = s.DB.QueryRow("SELECT group_concat(state) FROM jobs WHERE node_id=?", a.ID).Scan(&beforeJobs)
	_, err = s.DeleteNode(a.ID, model.NodeDeleteOptions{ExpectedRevision: a.Revision})
	var conflict *NodeSubscriptionConflict
	if !errors.As(err, &conflict) || !errors.Is(err, ErrConflict) || len(conflict.Templates) != 2 {
		t.Fatal("referenced node deletion was not blocked", err)
	}
	current, _ := s.Node(a.ID)
	var afterJobs string
	_ = s.DB.QueryRow("SELECT group_concat(state) FROM jobs WHERE node_id=?", a.ID).Scan(&afterJobs)
	var retirements int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM node_retirements WHERE node_id=?", a.ID).Scan(&retirements)
	if current.Lifecycle != "active" || current.Revision != a.Revision || current.DeletedAt != nil || beforeJobs != afterJobs || retirements != 0 {
		t.Fatal("blocked deletion mutated node, jobs or retirement")
	}
	// Removing one reference is insufficient; even a hidden disabled template
	// must be explicitly edited before the node can be retired.
	active.NodeIDs, active.DefaultNodeID = []string{b.ID}, b.ID
	if _, err = s.SaveSubscriptionTemplate(active.ID, active, active.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteNode(a.ID, model.NodeDeleteOptions{ExpectedRevision: a.Revision}); !errors.As(err, &conflict) || len(conflict.Templates) != 1 || conflict.Templates[0].ID != disabled.ID {
		t.Fatal("remaining hidden reference ignored", err)
	}
	disabled.NodeIDs, disabled.DefaultNodeID = []string{}, ""
	if _, err = s.SaveSubscriptionTemplate(disabled.ID, disabled, disabled.Revision); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteNode(a.ID, model.NodeDeleteOptions{ExpectedRevision: a.Revision})
	if err != nil || deleted.Lifecycle != "retiring" {
		t.Fatal("unreferenced node cannot retire", err)
	}
	other, _ := s.Node(b.ID)
	if other.Lifecycle != "active" {
		t.Fatal("other node was affected")
	}
}

func TestNodeDeletionAlsoBlocksDanglingDefaultReference(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "old-default")
	p, err := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "old template", ValidityDays: 30, MaxConnections: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a previous version's inconsistent template.
	if _, err = s.DB.Exec("UPDATE portal_templates SET config=json_set(config,'$.defaultNodeId',?) WHERE id=?", n.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision})
	if err != nil {
		t.Fatal("obsolete default blocked deletion", err)
	}
}

func TestTemplateAssignmentRacingNodeDeletionCannotLeaveReference(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "racing-node")
	if _, err := s.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]' WHERE id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var saveErr, deleteErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, saveErr = s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "racing template", ValidityDays: 1, MaxConnections: 1, NodeIDs: []string{n.ID}, DefaultNodeID: n.ID}, 0)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, deleteErr = s.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision})
	}()
	close(start)
	wg.Wait()
	if (saveErr == nil) == (deleteErr == nil) {
		t.Fatal("exactly one conflicting mutation must succeed", saveErr, deleteErr)
	}
	current, err := s.Node(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.NodeSubscriptionReferences(n.ID)
	if err != nil || current.Lifecycle != "active" && len(refs) > 0 {
		t.Fatal("deleted node retained a newly committed reference", err)
	}
}

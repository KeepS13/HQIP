package store

import (
	"errors"
	"testing"
)

// Application acknowledgement is business state, not a browser preference.
// Exercise both read paths and malicious/stale editor payloads without holders.
func TestTemplateAppliedRevisionLifecycle(t *testing.T) {
	s, _, _, p := allocationFixture(t, 2, 1)
	p.AppliedRevision = 999
	p, err := s.SaveSubscriptionTemplate("", p, 0)
	if err != nil || p.Revision != 1 || p.AppliedRevision != 0 {
		t.Fatal("creation accepted application state", p, err)
	}
	p.Name = "edited"
	p.AppliedRevision = 999
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil || p.Revision != 2 || p.AppliedRevision != 0 {
		t.Fatal("save acknowledged an edit", p, err)
	}
	if _, err = s.ApplySubscriptionTemplate(p.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	current, err := s.SubscriptionTemplate(p.ID)
	if err != nil || current.AppliedRevision != 0 {
		t.Fatal("conflict changed application state", current, err)
	}
	if result, err := s.ApplySubscriptionTemplate(p.ID, p.Revision); err != nil || result.AffectedSubscriptions != 0 {
		t.Fatal("empty audience apply", result, err)
	}
	current, err = s.SubscriptionTemplate(p.ID)
	if err != nil || current.AppliedRevision != 2 || current.Revision != 2 {
		t.Fatal("application state not persisted", current, err)
	}
	list, err := s.SubscriptionTemplates(false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		if item.ID == p.ID {
			found = true
			if item.AppliedRevision != 2 {
				t.Fatal("list lost application state", item)
			}
		}
	}
	if !found {
		t.Fatal("template absent from list")
	}
	// p is the editor snapshot from before apply. Saving it must retain the
	// acknowledgement while making its new version pending again.
	p.Name = "edited again"
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil || p.AppliedRevision != 2 || p.Revision != 3 {
		t.Fatal("stale editor overwrote acknowledgement", p, err)
	}
	p.AppliedRevision = 999
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil || p.AppliedRevision != 2 || p.Revision != 4 {
		t.Fatal("forged application state", p, err)
	}
}

func TestTemplateAppliedRevisionAcknowledgesShortfall(t *testing.T) {
	s, _, user, p := allocationFixture(t, 2, 1)
	assignAllocation(t, s, user.ID, p)
	assignAllocation(t, s, user.ID, p)
	p.NodesPerSubscription = 2
	p, err := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplySubscriptionTemplate(p.ID, p.Revision)
	if err != nil || len(result.AllocationShortfalls) != 2 {
		t.Fatal(result, err)
	}
	current, err := s.SubscriptionTemplate(p.ID)
	if err != nil || current.AppliedRevision != p.Revision {
		t.Fatal("shortfall lost successful application", current, err)
	}
}

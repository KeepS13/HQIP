package store

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestTemplateApplyResizesExistingHoldersWithoutResettingIdentity(t *testing.T) {
	s, _, user, p := allocationFixture(t, 6, 1)
	a := assignAllocation(t, s, user.ID, p)
	b := assignAllocation(t, s, user.ID, p)
	link, err := s.PortalAllSubscriptions(user.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	p.NodesPerSubscription, p.MaxConnections = 2, 5
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.UserSubscription(user.ID, a.ID)
	if err != nil || before.NodesPerSubscription != 1 {
		t.Fatal("save unexpectedly resized allocation", err)
	}
	result, err := s.ApplySubscriptionTemplate(p.ID, p.Revision)
	if err != nil || result.UpdatedSubscriptions != 2 || len(result.AllocationShortfalls) != 0 {
		t.Fatal(result, err)
	}
	seen := map[string]bool{}
	for _, original := range []model.UserSubscription{a, b} {
		current, err := s.UserSubscription(user.ID, original.ID)
		if err != nil || len(current.NodeIDs) != 2 || current.NodesPerSubscription != 2 || current.MaxConnections != 5 || !current.ExpiresAt.Equal(original.ExpiresAt) || current.CurrentPeriodID != original.CurrentPeriodID {
			t.Fatal(current, err)
		}
		if !slices.Contains(current.NodeIDs, original.NodeIDs[0]) {
			t.Fatal("existing assignment unnecessarily replaced")
		}
		for _, node := range current.NodeIDs {
			if seen[node] {
				t.Fatal("same user's subscriptions overlap")
			}
			seen[node] = true
		}
	}
	if again, err := s.ApplySubscriptionTemplate(p.ID, p.Revision); err != nil || again.UpdatedSubscriptions != 0 {
		t.Fatal("non-idempotent replay", again, err)
	}
	if current, err := s.PortalAllSubscriptions(user.ID, "get"); err != nil || current != link {
		t.Fatal("apply changed link", err)
	}
	p.NodesPerSubscription = 1
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplySubscriptionTemplate(p.ID, p.Revision-1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision applied", err)
	}
	if result, err = s.ApplySubscriptionTemplate(p.ID, p.Revision); err != nil || result.UpdatedSubscriptions != 2 {
		t.Fatal(result, err)
	}
	current, err := s.UserSubscription(user.ID, a.ID)
	if err != nil || len(current.NodeIDs) != 1 || current.NodeIDs[0] != a.NodeIDs[0] {
		t.Fatal(current, err)
	}
}

func TestTemplateApplyShortfallIsVisibleAndNewPoolFillsSavedTarget(t *testing.T) {
	s, _, user, p := allocationFixture(t, 2, 1)
	a := assignAllocation(t, s, user.ID, p)
	b := assignAllocation(t, s, user.ID, p)
	p.NodesPerSubscription = 2
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplySubscriptionTemplate(p.ID, p.Revision)
	if err != nil || len(result.AllocationShortfalls) != 2 {
		t.Fatal(result, err)
	}
	for _, old := range []model.UserSubscription{a, b} {
		current, err := s.UserSubscription(user.ID, old.ID)
		if err != nil || current.NodesPerSubscription != 2 || len(current.NodeIDs) != 1 {
			t.Fatal(current, err)
		}
	}
	for i := 0; i < 2; i++ {
		p.NodeIDs = append(p.NodeIDs, automaticTestNode(t, s, NewID()).ID)
	}
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil || len(p.AllocationShortfalls) != 0 {
		t.Fatal(p, err)
	}
	for _, old := range []model.UserSubscription{a, b} {
		current, err := s.UserSubscription(user.ID, old.ID)
		if err != nil || len(current.NodeIDs) != 2 {
			t.Fatal(current, err)
		}
	}
}

func TestTemplateApplyPendingCustomAndExpiredBoundaries(t *testing.T) {
	s, now, user, p := allocationFixture(t, 2, 1)
	expired := assignAllocation(t, s, user.ID, p)
	audienceExec(t, s, "UPDATE portal_subscriptions SET expires=? WHERE id=?", now.Unix()-1, expired.ID)
	p.NodeIDs = []string{}
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	pending := assignAllocation(t, s, user.ID, p)
	p.NodesPerSubscription = 3
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplySubscriptionTemplate(p.ID, p.Revision)
	if err != nil || result.AffectedSubscriptions != 1 || len(result.AllocationShortfalls) != 1 {
		t.Fatal(result, err)
	}
	current, err := s.UserSubscription(user.ID, pending.ID)
	if err != nil || current.Status != "pending" || current.NodesPerSubscription != 3 || !current.ExpiresAt.IsZero() {
		t.Fatal(current, err)
	}
	old, err := s.UserSubscription(user.ID, expired.ID)
	if err != nil || old.Status != "expired" || old.NodesPerSubscription != 1 {
		t.Fatal(old, err)
	}
}

func TestTemplateApplyRollbackDoesNotPartiallyResize(t *testing.T) {
	s, _, user, p := allocationFixture(t, 4, 1)
	a := assignAllocation(t, s, user.ID, p)
	b := assignAllocation(t, s, user.ID, p)
	p.NodesPerSubscription = 2
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if s.Postgres() {
		audienceExec(t, s, `CREATE FUNCTION fail_template_apply() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.template_applied' THEN RAISE EXCEPTION 'test rollback'; END IF; RETURN NEW; END $$`)
		audienceExec(t, s, `CREATE TRIGGER fail_template_apply BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION fail_template_apply()`)
	} else {
		audienceExec(t, s, `CREATE TRIGGER fail_template_apply BEFORE INSERT ON audit WHEN NEW.action='subscription.template_applied' BEGIN SELECT RAISE(ABORT,'test rollback'); END`)
	}
	if _, err = s.ApplySubscriptionTemplate(p.ID, p.Revision); err == nil {
		t.Fatal("injected failure committed")
	}
	for _, old := range []model.UserSubscription{a, b} {
		current, err := s.UserSubscription(user.ID, old.ID)
		if err != nil || current.NodesPerSubscription != 1 || !sameNodeSet(current.NodeIDs, old.NodeIDs) {
			t.Fatal("partial resize", current, err)
		}
	}
}

func TestTemplateApplyConcurrentPurchaseKeepsDistinctAllocations(t *testing.T) {
	s, _, user, p := allocationFixture(t, 10, 1)
	assignAllocation(t, s, user.ID, p)
	assignAllocation(t, s, user.ID, p)
	p.NodesPerSubscription = 2
	var err error
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errors := make(chan error, 3)
	for i := 0; i < 3; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			var e error
			if i == 0 {
				_, e = s.AssignSubscription(user.ID, p.ID, NewID(), -1, p.Revision)
			} else {
				_, e = s.ApplySubscriptionTemplate(p.ID, p.Revision)
			}
			errors <- e
		}(i)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	subs, err := s.UserSubscriptions(user.ID)
	if err != nil || len(subs) != 3 {
		t.Fatal(subs, err)
	}
	seen := map[string]bool{}
	for _, sub := range subs {
		if sub.NodesPerSubscription != 2 || len(sub.NodeIDs) != 2 {
			t.Fatal(sub)
		}
		for _, node := range sub.NodeIDs {
			if seen[node] {
				t.Fatal("duplicate allocation", node)
			}
			seen[node] = true
		}
	}
}

func TestTemplateApplyRemovedNodeRevokesLeaseWithoutLosingReservations(t *testing.T) {
	s, _, user, p := allocationFixture(t, 3, 3)
	if err := s.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	sub := assignAllocation(t, s, user.ID, p)
	if _, err := s.PortalSubscription(user.ID, sub.ID, "create"); err != nil {
		t.Fatal(err)
	}
	node, err := s.Node(sub.NodeIDs[2])
	if err != nil {
		t.Fatal(err)
	}
	password, err := s.PortalCredential(user.ID, sub.ID, node.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	lease := acquire(t, s, node, password, "a")
	before, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.NodesPerSubscription = 1
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplySubscriptionTemplate(p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || current.ReservedBytes != before.ReservedBytes || current.UsedBytes != before.UsedBytes || !current.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatal("ledger changed", current, err)
	}
	if _, err = s.RenewLease(node.ID, lease.ID, model.LeaseUsage{}); !errors.Is(err, ErrEntitlement) {
		t.Fatal("removed node retained forwarding", err)
	}
}

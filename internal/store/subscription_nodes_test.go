package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestSubscriptionNodesPreserveUsageIdentityAndOtherGrants(t *testing.T) {
	s, _, u, p := allocationFixture(t, 4, 2)
	a, b := assignAllocation(t, s, u.ID, p), assignAllocation(t, s, u.ID, p)
	outside := automaticTestNode(t, s, "outside pool")
	other, err := s.RegisterPortal("other-node-edit@example.com", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	c := assignAllocation(t, s, other.ID, p)
	if err = s.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Node(a.NodeIDs[0])
	keep, _ := s.Node(a.NodeIDs[1])
	oldPassword := subscriptionPassword(t, s, u.ID, a.ID, old.ID)
	keptPassword := subscriptionPassword(t, s, u.ID, a.ID, keep.ID)
	oldLease, keptLease := acquire(t, s, old, oldPassword, "a"), acquire(t, s, keep, keptPassword, "b")
	_ = renew(t, s, oldLease, 123, 456, false)
	session := nativeSubscriptionSession(t, s, u.ID, a.ID)
	before, _ := s.UserSubscription(u.ID, a.ID)
	var importHash string
	_ = s.DB.QueryRow("SELECT subscription_hash FROM portal_subscriptions WHERE id=?", a.ID).Scan(&importHash)
	updated, err := s.UpdateSubscriptionNodes(u.ID, a.ID, []string{keep.ID, outside.ID}, before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	expected := *before
	expected.NodeIDs, expected.Revision = updated.NodeIDs, before.Revision+1
	if !reflect.DeepEqual(updated, expected) {
		t.Fatalf("unrelated entitlement changed: %+v vs %+v", updated, expected)
	}
	for _, sub := range []model.UserSubscription{b, c} {
		actual, e := s.UserSubscription(sub.UserID, sub.ID)
		if e != nil || !reflect.DeepEqual(*actual, sub) {
			t.Fatal("other subscription changed", e)
		}
	}
	var hash string
	_ = s.DB.QueryRow("SELECT subscription_hash FROM portal_subscriptions WHERE id=?", a.ID).Scan(&hash)
	if hash != importHash {
		t.Fatal("import URL rotated")
	}
	if _, err = s.PortalCredential(u.ID, a.ID, old.ID, "subscription"); !errors.Is(err, ErrEntitlement) {
		t.Fatal("removed node still authorized", err)
	}
	if _, err = s.AcquireLease(old.ID, strings.Repeat("c", 32), oldPassword); !errors.Is(err, ErrEntitlement) {
		t.Fatal("old credential admitted", err)
	}
	var revoked bool
	_ = s.DB.QueryRow("SELECT revoked FROM portal_leases WHERE id=?", oldLease.ID).Scan(&revoked)
	if !revoked {
		t.Fatal("old lease not revoked")
	}
	_ = s.DB.QueryRow("SELECT revoked FROM portal_leases WHERE id=?", keptLease.ID).Scan(&revoked)
	if revoked {
		t.Fatal("retained connection revoked")
	}
	closed := renew(t, s, oldLease, 123, 456, true)
	if !closed.Closed {
		t.Fatal("old connection did not settle")
	}
	settled, _ := s.UserSubscription(u.ID, a.ID)
	if settled.Occupied != 1 || settled.UsedBytes != 579 {
		t.Fatal("settlement changed usage or slots", settled)
	}
	for _, scope := range []string{session, "subscription"} {
		profile, _, e := s.PortalClientProfile(u.ID, scope, a.ID)
		if e != nil || len(profile.Nodes) != 2 {
			t.Fatal("profile", e, profile)
		}
		for _, n := range profile.Nodes {
			if n.ID == old.ID {
				t.Fatal("profile retains removed node")
			}
		}
	}
	beforeUser, _ := s.PortalUser(u.ID)
	noOp, err := s.UpdateSubscriptionNodes(u.ID, a.ID, []string{outside.ID, keep.ID}, updated.Revision)
	afterUser, _ := s.PortalUser(u.ID)
	if err != nil || noOp.Revision != updated.Revision || beforeUser.Revision != afterUser.Revision {
		t.Fatal("reordered write was not no-op", err)
	}
	var audits int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='subscription.nodes_updated'").Scan(&audits)
	if audits != 1 {
		t.Fatal("audit count", audits)
	}
}

func TestSubscriptionNodesCountsTemplateReconciliationAndDeletion(t *testing.T) {
	s, _, u, p := allocationFixture(t, 4, 1)
	a := assignAllocation(t, s, u.ID, p)
	outside := automaticTestNode(t, s, "outside")
	outside, _ = s.Node(outside.ID)
	a, err := s.UpdateSubscriptionNodes(u.ID, a.ID, []string{a.NodeIDs[0], outside.ID}, a.Revision)
	if err != nil || a.NodesPerSubscription != 2 {
		t.Fatal(a, err)
	}
	refs, err := s.NodeUserSubscriptionReferences(outside.ID)
	if err != nil || len(refs) != 1 || refs[0].ID != a.ID {
		t.Fatal(refs, err)
	}
	_, err = s.DeleteNode(outside.ID, model.NodeDeleteOptions{ExpectedRevision: outside.Revision})
	var conflict *NodeSubscriptionConflict
	if !errors.As(err, &conflict) || len(conflict.UserSubscriptions) != 1 || len(conflict.Templates) != 0 {
		t.Fatal("external node deletion not blocked", err)
	}
	// Changing metadata alone does not discard the manual nodes/count.
	p.Name = "renamed"
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := s.UserSubscription(u.ID, a.ID)
	if !sameNodeSet(kept.NodeIDs, a.NodeIDs) || kept.NodesPerSubscription != 2 {
		t.Fatal("metadata overwrote manual assignment")
	}
	newPool := automaticTestNode(t, s, "new pool")
	p.NodeIDs = append(p.NodeIDs, newPool.ID)
	preview, err := s.PreviewSubscriptionTemplate(p.ID, p)
	if err != nil || len(preview.AllocationShortfalls) != 0 {
		t.Fatal(preview, err)
	}
	p, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	kept, _ = s.UserSubscription(u.ID, a.ID)
	if kept.NodesPerSubscription != 2 || len(kept.NodeIDs) != 2 {
		t.Fatal("manual target lost", kept)
	}
	for _, id := range kept.NodeIDs {
		if id == outside.ID {
			t.Fatal("out-of-pool node not reconciled")
		}
	}
	if _, err = s.DeleteNode(outside.ID, model.NodeDeleteOptions{ExpectedRevision: outside.Revision}); err != nil {
		t.Fatal(err)
	}
	// Reduce, then clear. Zero is a persistent target, not a request to refill.
	a, err = s.UpdateSubscriptionNodes(u.ID, a.ID, kept.NodeIDs[:1], kept.Revision)
	if err != nil || a.NodesPerSubscription != 1 {
		t.Fatal(a, err)
	}
	a, err = s.UpdateSubscriptionNodes(u.ID, a.ID, []string{}, a.Revision)
	if err != nil || a.NodesPerSubscription != 0 || len(a.NodeIDs) != 0 {
		t.Fatal(a, err)
	}
	p.NodeIDs = []string{newPool.ID}
	if _, err = s.SaveSubscriptionTemplate(p.ID, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	kept, _ = s.UserSubscription(u.ID, a.ID)
	if kept.NodesPerSubscription != 0 || len(kept.NodeIDs) != 0 {
		t.Fatal("empty allocation refilled", kept)
	}
}

func TestSubscriptionNodesValidationAndAvailability(t *testing.T) {
	for _, scenario := range []string{"missing-list", "duplicate", "over-limit", "foreign", "stale", "expired", "revoked", "disabled", "exhausted", "offline-new", "offline-retained", "not-ready", "incapable", "retiring", "server-expired", "server-exhausted", "unknown", "occupied", "reserved"} {
		t.Run(scenario, func(t *testing.T) {
			s, now, u, p := allocationFixture(t, 3, 1)
			a := assignAllocation(t, s, u.ID, p)
			n := automaticTestNode(t, s, "external")
			ids, owner, revision, want := []string{n.ID}, u.ID, a.Revision, error(nil)
			switch scenario {
			case "missing-list":
				ids = nil
				want = ErrSubscriptionNodes
			case "duplicate":
				ids = []string{n.ID, n.ID}
				want = ErrSubscriptionNodes
			case "over-limit":
				ids = make([]string, 1001)
				want = ErrSubscriptionNodes
			case "foreign":
				owner = "wrong-user"
				want = ErrNotFound
			case "stale":
				revision++
				want = ErrConflict
			case "expired":
				*now = a.ExpiresAt
				want = ErrSubscriptionNodesState
			case "revoked":
				audienceExec(t, s, "UPDATE portal_subscriptions SET revoked=1 WHERE id=?", a.ID)
				want = ErrSubscriptionNodesState
			case "disabled":
				audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", u.ID)
			case "exhausted":
				audienceExec(t, s, "UPDATE portal_templates SET config=json_set(config,'$.quotaBytes',1) WHERE id=?", p.ID)
			case "offline-new":
				audienceExec(t, s, "UPDATE nodes SET last_seen=0 WHERE id=?", n.ID)
				want = ErrConflict
			case "offline-retained":
				audienceExec(t, s, "UPDATE nodes SET last_seen=0 WHERE id=?", a.NodeIDs[0])
				ids = append(ids, a.NodeIDs...)
			case "not-ready":
				if err := s.SetStatus(n.ID, "pending", "test", false); err != nil {
					t.Fatal(err)
				}
				want = ErrConflict
			case "incapable":
				audienceExec(t, s, "UPDATE nodes SET capabilities='[]' WHERE id=?", n.ID)
				want = ErrConflict
			case "retiring":
				audienceExec(t, s, "UPDATE nodes SET lifecycle='retiring' WHERE id=?", n.ID)
				want = ErrConflict
			case "server-expired":
				automaticTestBilling(t, s, n, now.Add(-48*time.Hour), now.Add(-time.Hour))
				want = ErrConflict
			case "server-exhausted":
				automaticTestQuota(t, s, n, 0)
				want = ErrConflict
			case "unknown":
				ids = []string{"nonexistent-node"}
				want = ErrSubscriptionNodes
			case "occupied":
				b := assignAllocation(t, s, u.ID, p)
				ids = b.NodeIDs
				want = ErrConflict
			case "reserved":
				enableAllocationPayments(t, s)
				order, err := s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: NewID(), TemplateID: p.ID, TemplateRevision: p.Revision}, paymentTestClient())
				if err != nil {
					t.Fatal(err)
				}
				_, ids = allocationReservation(t, s, order.ID)
				want = ErrConflict
			}
			before, _ := s.UserSubscription(u.ID, a.ID)
			view, err := s.SubscriptionNodes(u.ID, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "offline-retained" {
				for _, option := range view.Nodes {
					if option.ID == a.NodeIDs[0] && (!option.Selectable || option.Reason == "") {
						t.Fatal("offline retained option", option)
					}
				}
			}
			updated, err := s.UpdateSubscriptionNodes(owner, a.ID, ids, revision)
			if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			if want != nil {
				after, _ := s.UserSubscription(u.ID, a.ID)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("failed save changed subscription")
				}
			} else if len(updated.NodeIDs) != len(ids) {
				t.Fatal(updated)
			}
		})
	}
}

func TestSubscriptionNodesConcurrentInventoryAndDelete(t *testing.T) {
	for _, scenario := range []string{"same-revision", "two-grants", "purchase", "delete", "template"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, u, p := allocationFixture(t, 2, 1)
			a := assignAllocation(t, s, u.ID, p)
			n := automaticTestNode(t, s, "race target")
			n, _ = s.Node(n.ID)
			other, err := reopenFixture(s)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			other.Now = s.Now
			var b model.UserSubscription
			if scenario == "two-grants" {
				b = assignAllocation(t, s, u.ID, p)
			}
			if scenario == "purchase" {
				enableAllocationPayments(t, s)
				for _, id := range p.NodeIDs {
					if id != a.NodeIDs[0] {
						n, _ = s.Node(id)
					}
				}
			}
			if scenario == "template" {
				p.NodeIDs = append(p.NodeIDs, automaticTestNode(t, other, "pool addition").ID)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, e := s.UpdateSubscriptionNodes(u.ID, a.ID, []string{n.ID}, a.Revision)
				results <- e
			}()
			go func() {
				defer wg.Done()
				<-start
				var e error
				switch scenario {
				case "same-revision":
					_, e = other.UpdateSubscriptionNodes(u.ID, a.ID, []string{}, a.Revision)
				case "two-grants":
					_, e = other.UpdateSubscriptionNodes(u.ID, b.ID, []string{n.ID}, b.Revision)
				case "purchase":
					_, e = other.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: NewID(), TemplateID: p.ID, TemplateRevision: p.Revision}, paymentTestClient())
				case "delete":
					_, e = other.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision})
				case "template":
					_, e = other.SaveSubscriptionTemplate(p.ID, p, p.Revision)
				}
				results <- e
			}()
			close(start)
			wg.Wait()
			close(results)
			success := 0
			for e := range results {
				if e == nil {
					success++
				} else if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrSubscriptionStock) {
					t.Fatal("unexpected race failure", e)
				}
			}
			if (scenario == "same-revision" || scenario == "two-grants" || scenario == "delete") && success != 1 {
				t.Fatal("expected one winner", success)
			}
			tx, e := s.DB.Begin()
			if e != nil {
				t.Fatal(e)
			}
			holders, e := allocationHolders(tx, s.Now())
			tx.Rollback()
			if e != nil {
				t.Fatal(e)
			}
			used := map[string]bool{}
			for _, h := range holders {
				if h.user == u.ID && h.template == p.ID {
					for _, id := range h.nodes {
						if used[id] {
							t.Fatal("duplicate allocation", id)
						}
						used[id] = true
					}
				}
			}
			if scenario == "delete" {
				node, _ := s.Node(n.ID)
				if used[n.ID] && node.Lifecycle != "active" {
					t.Fatal("deleted node allocated")
				}
			}
			if scenario == "template" && used[n.ID] {
				t.Fatal("template update failed to reconcile external node")
			}
		})
	}
}

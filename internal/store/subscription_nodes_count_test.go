package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// A node replacement must not resize an existing grant, even when a template's
// default count has changed since purchase or its pool currently has a shortfall.
func TestSubscriptionNodesRequireOriginalAllocationCount(t *testing.T) {
	for _, required := range []int{1, 2} {
		for _, chosen := range []int{0, 1, 2, 3} {
			if chosen == required {
				continue
			}
			t.Run(fmt.Sprintf("required-%d-selected-%d", required, chosen), func(t *testing.T) {
				s, _, user, template := allocationFixture(t, 4, required)
				sub := assignAllocation(t, s, user.ID, template)
				beforeUser, err := s.PortalUser(user.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.UpdateSubscriptionNodes(user.ID, sub.ID, template.NodeIDs[:chosen], sub.Revision)
				if !errors.Is(err, ErrSubscriptionNodes) {
					t.Fatalf("resizing to %d accepted: %v", chosen, err)
				}
				after, err := s.UserSubscription(user.ID, sub.ID)
				if err != nil || !reflect.DeepEqual(*after, sub) {
					t.Fatal("rejected replacement changed subscription", after, err)
				}
				afterUser, err := s.PortalUser(user.ID)
				if err != nil || afterUser.Revision != beforeUser.Revision {
					t.Fatal("rejected replacement changed user revision", afterUser, err)
				}
				var audits int
				if err = s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='subscription.nodes_updated'").Scan(&audits); err != nil || audits != 0 {
					t.Fatal("rejected replacement created an audit", audits, err)
				}
			})
		}
	}
}

func TestSubscriptionNodesKeepPurchasedCountAfterTemplateChange(t *testing.T) {
	for _, required := range []int{1, 2} {
		t.Run(fmt.Sprint(required), func(t *testing.T) {
			s, _, user, template := allocationFixture(t, 4, required)
			sub := assignAllocation(t, s, user.ID, template)
			template.NodesPerSubscription = 3
			if _, err := s.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
				t.Fatal(err)
			}
			before, err := s.UserSubscription(user.ID, sub.ID)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for i := 0; i < required; i++ {
				ids = append(ids, automaticTestNode(t, s, fmt.Sprintf("replacement-%d", i)).ID)
			}
			updated, err := s.UpdateSubscriptionNodes(user.ID, sub.ID, ids, before.Revision)
			if err != nil || updated.NodesPerSubscription != required || !sameNodeSet(updated.NodeIDs, ids) {
				t.Fatal("replacement used the template's new default", updated, err)
			}
		})
	}
}

func TestSubscriptionNodesRepairShortfallWithoutReducingTarget(t *testing.T) {
	s, _, user, template := allocationFixture(t, 3, 2)
	sub := assignAllocation(t, s, user.ID, template)
	template.NodeIDs = sub.NodeIDs[:1]
	if _, err := s.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	before, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || len(before.NodeIDs) != 1 || before.NodesPerSubscription != 2 {
		t.Fatal("expected an allocation shortfall", before, err)
	}
	if _, err = s.UpdateSubscriptionNodes(user.ID, sub.ID, before.NodeIDs, before.Revision); !errors.Is(err, ErrSubscriptionNodes) {
		t.Fatal("saving shortfall silently reduced the target", err)
	}
	replacement := automaticTestNode(t, s, "shortfall replacement")
	updated, err := s.UpdateSubscriptionNodes(user.ID, sub.ID, []string{before.NodeIDs[0], replacement.ID}, before.Revision)
	if err != nil || len(updated.NodeIDs) != 2 || updated.NodesPerSubscription != 2 {
		t.Fatal("could not repair to original target", updated, err)
	}
}

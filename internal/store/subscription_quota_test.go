package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestSubscriptionAdmissionFloorMatchesPortalAndClientStatus(t *testing.T) {
	floor := model.MinimumSubscriptionLeaseBytes
	for _, remaining := range []int64{0, 1, 478, floor - 1, floor, floor + 1} {
		t.Run(fmt.Sprint(remaining), func(t *testing.T) {
			quota := 2 * floor
			s, _, user, _, node, password := portalFixture(t, 2, &quota)
			id := user.Subscriptions[0].ID
			session := nativeSubscriptionSession(t, s, user.ID, id)
			if _, err := s.DB.Exec("UPDATE portal_usage SET upload=? WHERE subscription_id=?", quota-remaining, id); err != nil {
				t.Fatal(err)
			}
			want := "active"
			if remaining < floor {
				want = "exhausted"
			}
			portalUser, err := s.PortalUser(user.ID)
			if err != nil || len(portalUser.Subscriptions) != 1 || portalUser.Subscriptions[0].Status != want {
				t.Fatalf("portal status: %+v %v", portalUser.Subscriptions, err)
			}
			for _, scope := range []string{"subscription", session} {
				profile, sub, err := s.PortalClientProfile(user.ID, scope, id)
				if err != nil || sub == nil || sub.Status != want || sub.UsedBytes != quota-remaining || sub.ReservedBytes != 0 {
					t.Fatalf("client profile must retain exact counters and status: %+v %v", sub, err)
				}
				if (len(profile.Nodes) > 0) != (want == "active") {
					t.Fatalf("profile nodes contradict status: %+v", profile)
				}
			}
			lease, err := s.AcquireLease(node.ID, strings.Repeat("a", 32), password)
			if remaining < floor {
				if !errors.Is(err, ErrEntitlement) {
					t.Fatalf("sub-floor admission accepted: %+v %v", lease, err)
				}
			} else if err != nil || lease.BudgetBytes != remaining {
				t.Fatalf("usable boundary rejected or rounded: %+v %v", lease, err)
			}
			after, err := s.UserSubscription(user.ID, id)
			if err != nil || after.UsedBytes != quota-remaining || *after.QuotaBytes != quota {
				t.Fatalf("status/admission changed real usage: %+v %v", after, err)
			}
		})
	}
}

func TestSubscriptionLiveTailRemainsUsableUntilFinalSettlement(t *testing.T) {
	quota := 2 * model.MinimumSubscriptionLeaseBytes
	s, _, user, _, node, password := portalFixture(t, 2, &quota)
	id := user.Subscriptions[0].ID
	lease := acquire(t, s, node, password, "a")
	lease = renew(t, s, lease, quota-478, 0, false)
	sub, err := s.UserSubscription(user.ID, id)
	if err != nil || sub.Status != "active" || sub.ReservedBytes != 478 || sub.UsedBytes != quota-478 {
		t.Fatalf("live tail was exhausted or reservation charged: %+v %v", sub, err)
	}
	profile, sub, err := s.PortalClientProfile(user.ID, "subscription", id)
	if err != nil || sub.Status != "active" || len(profile.Nodes) != 1 {
		t.Fatalf("profile would stop the existing connection: %+v %+v %v", profile, sub, err)
	}
	replay, err := s.AcquireLease(node.ID, strings.Repeat("a", 32), password)
	if err != nil || replay.ID != lease.ID || replay.BudgetBytes != lease.BudgetBytes {
		t.Fatalf("tail-budget admission replay was not idempotent: %+v %v", replay, err)
	}
	if _, err = s.AcquireLease(node.ID, strings.Repeat("b", 32), password); !errors.Is(err, ErrEntitlement) {
		t.Fatal("new connection acquired a sub-floor/reserved budget", err)
	}
	lease = renew(t, s, lease, quota-478, 1, false)
	_ = renew(t, s, lease, quota-478, 1, false)
	sub, err = s.UserSubscription(user.ID, id)
	if err != nil || sub.Status != "active" || sub.UsedBytes != quota-477 || sub.ReservedBytes != 477 {
		t.Fatalf("small-tail renewal or replay lost accounting: %+v %v", sub, err)
	}
	lease = renew(t, s, lease, quota-478, 2, true)
	_ = renew(t, s, lease, quota-478, 2, true)
	sub, err = s.UserSubscription(user.ID, id)
	if err != nil || sub.Status != "exhausted" || sub.UsedBytes != quota-476 || sub.ReservedBytes != 0 || sub.Occupied != 0 {
		t.Fatalf("closed-tail status or exact final settlement wrong: %+v %v", sub, err)
	}
}

func TestSubscriptionUnsettledReservationIsNotUsage(t *testing.T) {
	quota := 2 * model.MinimumSubscriptionLeaseBytes
	s, now, user, _, node, password := portalFixture(t, 2, &quota)
	lease := acquire(t, s, node, password, "a")
	*now = now.Add(121 * time.Second)
	sub, err := s.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if err != nil || sub.Status != "active" || sub.UsedBytes != 0 || sub.ReservedBytes != quota {
		t.Fatalf("unsettled reservation was treated as usage: %+v %v", sub, err)
	}
	if _, err = s.AcquireLease(node.ID, strings.Repeat("b", 32), password); !errors.Is(err, ErrEntitlement) {
		t.Fatal("reserved quota was issued twice", err)
	}
	_ = renew(t, s, lease, 0, 478, true)
	next := acquire(t, s, node, password, "b")
	if next.BudgetBytes != quota-478 {
		t.Fatalf("released reservation was not reusable: %+v", next)
	}
}

func TestSubscriptionNewLeaseFloorUsesUnreservedBalance(t *testing.T) {
	floor := model.MinimumSubscriptionLeaseBytes
	for _, free := range []int64{floor - 1, floor, floor + 1} {
		t.Run(fmt.Sprint(free), func(t *testing.T) {
			quota := leaseWindow(1_000_000) + free
			s, _, user, _, node, password := portalFixture(t, 2, &quota)
			first := acquire(t, s, node, password, "a")
			if quota-first.BudgetBytes != free {
				t.Fatal("fixture did not leave requested unreserved balance")
			}
			next, err := s.AcquireLease(node.ID, strings.Repeat("b", 32), password)
			if free < floor {
				if !errors.Is(err, ErrEntitlement) {
					t.Fatalf("new lease received unusable free tail: %+v %v", next, err)
				}
			} else if err != nil || next.BudgetBytes != free {
				t.Fatalf("new lease at valid free boundary failed: %+v %v", next, err)
			}
			sub, err := s.UserSubscription(user.ID, first.SubscriptionID)
			if err != nil || sub.Status != "active" || sub.UsedBytes != 0 || sub.ReservedBytes > quota {
				t.Fatalf("unreserved budget gate corrupted active accounting: %+v %v", sub, err)
			}
		})
	}
}

func TestSubscriptionDeadLeasesDoNotKeepTailActive(t *testing.T) {
	for _, state := range []string{"expired", "revoked", "fully-consumed"} {
		t.Run(state, func(t *testing.T) {
			quota := 2 * model.MinimumSubscriptionLeaseBytes
			s, now, user, _, node, password := portalFixture(t, 2, &quota)
			lease := acquire(t, s, node, password, "a")
			lease = renew(t, s, lease, quota-478, 0, false)
			switch state {
			case "expired":
				*now = now.Add(121 * time.Second)
			case "revoked":
				if err := s.RevokePortalConnection(user.ID, lease.ID, ""); err != nil {
					t.Fatal(err)
				}
			case "fully-consumed":
				// Model a grant whose full budget has been reported, with an
				// unallocated tail remaining in the subscription.
				if _, err := s.DB.Exec("UPDATE portal_leases SET budget=upload+download WHERE id=?", lease.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB.Exec("UPDATE portal_usage SET reserved=0 WHERE subscription_id=?", lease.SubscriptionID); err != nil {
					t.Fatal(err)
				}
			}
			sub, err := s.UserSubscription(user.ID, lease.SubscriptionID)
			if err != nil || sub.Status != "exhausted" || sub.UsedBytes != quota-478 {
				t.Fatalf("dead lease kept tail active or changed usage: %+v %v", sub, err)
			}
		})
	}
}

func TestSubscriptionTailPolicyPreservesStatePriorityAndUnlimited(t *testing.T) {
	for _, state := range []string{"revoked", "disabled", "expired"} {
		t.Run(state, func(t *testing.T) {
			quota := 2 * model.MinimumSubscriptionLeaseBytes
			s, now, user, _, node, password := portalFixture(t, 2, &quota)
			lease := acquire(t, s, node, password, "a")
			lease = renew(t, s, lease, quota-478, 0, false)
			*now = now.Add(48 * time.Hour)
			if state == "revoked" {
				if _, err := s.DB.Exec("UPDATE portal_subscriptions SET revoked=1 WHERE id=?", lease.SubscriptionID); err != nil {
					t.Fatal(err)
				}
			}
			if state != "expired" {
				if _, err := s.DB.Exec("UPDATE portal_users SET enabled=0 WHERE id=?", user.ID); err != nil {
					t.Fatal(err)
				}
			}
			sub, err := s.UserSubscription(user.ID, lease.SubscriptionID)
			if err != nil || sub.Status != state || sub.UsedBytes != quota-478 || sub.ReservedBytes != 478 {
				t.Fatalf("state priority or counters changed: %+v %v", sub, err)
			}
		})
	}
	s, _, user, _, node, password := portalFixture(t, 2, nil)
	lease := acquire(t, s, node, password, "a")
	_ = renew(t, s, lease, 0, lease.BudgetBytes, true)
	sub, err := s.UserSubscription(user.ID, lease.SubscriptionID)
	if err != nil || sub.Status != "active" || sub.QuotaBytes != nil || sub.UsedBytes != lease.BudgetBytes || sub.ReservedBytes != 0 {
		t.Fatalf("unlimited subscription affected by admission floor: %+v %v", sub, err)
	}
	_ = acquire(t, s, node, password, "b")
}

func TestSubscriptionLiveTailIsSharedAcrossNodesButIsolatedAcrossCopies(t *testing.T) {
	quota := 2 * leaseWindow(1_000_000)
	s, _, user, template, firstNode, firstPassword := portalFixture(t, 2, &quota)
	secondNode := newNode(t, s, "tail-second-node")
	if err := s.SetStatus(secondNode.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]' WHERE id=?", secondNode.ID); err != nil {
		t.Fatal(err)
	}
	template.NodeIDs = append(template.NodeIDs, secondNode.ID)
	var err error
	template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	id := user.Subscriptions[0].ID
	secondPassword, err := s.PortalCredential(user.ID, id, secondNode.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	first := acquire(t, s, firstNode, firstPassword, "a")
	second := acquire(t, s, secondNode, secondPassword, "b")
	if first.BudgetBytes+second.BudgetBytes != quota {
		t.Fatal("fixture did not reserve the quota on two nodes")
	}
	first = renew(t, s, first, first.BudgetBytes-2000, 0, false)
	second = renew(t, s, second, second.BudgetBytes-2000, 0, false)
	check := func(status string, occupied int, reserved int64) {
		t.Helper()
		sub, e := s.UserSubscription(user.ID, id)
		if e != nil || sub.Status != status || sub.Occupied != occupied || sub.UsedBytes != quota-4000 || sub.ReservedBytes != reserved {
			t.Fatalf("cross-node tail status/counters: %+v %v", sub, e)
		}
	}
	check("active", 2, 4000)
	other := grantAnother(t, s, user.ID, template, "separate-tail-copy")
	otherPassword := subscriptionPassword(t, s, user.ID, other.ID, firstNode.ID)
	otherLease := acquire(t, s, firstNode, otherPassword, "c")
	_ = renew(t, s, first, first.UploadBytes, 0, true)
	check("active", 1, 2000)
	if _, err = s.AcquireLease(firstNode.ID, strings.Repeat("d", 32), firstPassword); !errors.Is(err, ErrEntitlement) {
		t.Fatal("released sub-floor balance admitted another connection", err)
	}
	_ = renew(t, s, second, second.UploadBytes, 0, true)
	check("exhausted", 0, 0)
	// Another copy's live lease must not keep the exhausted copy active, and
	// settling the first copy must not consume or release the other's budget.
	remaining, err := s.UserSubscription(user.ID, other.ID)
	if err != nil || remaining.Status != "active" || remaining.UsedBytes != 0 || remaining.ReservedBytes != otherLease.BudgetBytes || remaining.Occupied != 1 {
		t.Fatalf("independent copy affected by tail settlement: %+v %v", remaining, err)
	}
}

package store

import (
	"errors"
	"strings"
	"testing"
)

func TestAccountImportSurvivesEmptyPurchaseReplacementAndSingleRotation(t *testing.T) {
	s, now, user, p := allocationFixture(t, 3, 1)
	link, err := s.PortalAllSubscriptions(user.ID, "create")
	if err != nil || link == "" {
		t.Fatal("empty account link", err)
	}
	check := func(count int) {
		t.Helper()
		for _, action := range []string{"get", "create"} {
			current, e := s.PortalAllSubscriptions(user.ID, action)
			if e != nil || current != link {
				t.Fatal("link changed", e)
			}
		}
		owner, ids, e := s.ResolvePortalImport(importToken(link))
		if e != nil || owner != user.ID || len(ids) != count {
			t.Fatal("membership", ids, e)
		}
	}
	check(0)
	first := assignAllocation(t, s, user.ID, p)
	check(1)
	single, err := s.PortalSubscription(user.ID, first.ID, "create")
	if err != nil || single == link {
		t.Fatal("single scope", err)
	}
	second := assignAllocation(t, s, user.ID, p)
	check(2)
	if _, err = s.PortalSubscription(user.ID, first.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	check(2)
	if _, _, err = s.ResolvePortalImport(importToken(single)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old single survived")
	}
	audienceExec(t, s, "UPDATE portal_subscriptions SET expires=? WHERE user_id=?", now.Unix()-1, user.ID)
	check(0)
	assignAllocation(t, s, user.ID, p)
	check(1)
	// Knowing the account ID cannot forge the random capability or mix scopes.
	for _, token := range []string{strings.Replace(importToken(link), user.ID, second.ID, 1), "all." + user.ID + "." + strings.Repeat("x", 43)} {
		if _, _, err = s.ResolvePortalImport(token); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("forged account token", err)
		}
	}
}

func TestLegacySingleAndSnapshotImportsRemainIsolated(t *testing.T) {
	s, _, user, p := allocationFixture(t, 3, 1)
	first := assignAllocation(t, s, user.ID, p)
	second := assignAllocation(t, s, user.ID, p)
	a, err := s.PortalSubscription(user.ID, first.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PortalSubscription(user.ID, second.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	legacy := importToken(a) + "." + importToken(b)
	if _, ids, err := s.ResolvePortalImport(legacy); err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	if _, err = s.PortalSubscription(user.ID, first.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ResolvePortalImport(legacy); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("legacy snapshot survived member rotation", err)
	}
}

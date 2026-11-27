package store

import (
	"context"
	"errors"
	"testing"
)

func TestPrepareAccountImportsPreservesOldLinksAndReplays(t *testing.T) {
	s, _, user, p := allocationFixture(t, 3, 1)
	first := assignAllocation(t, s, user.ID, p)
	old, err := s.PortalSubscription(user.ID, first.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.PreparePortalAccountImports(context.Background())
	if err != nil || result.Users != 1 || result.Created != 1 || result.Existing != 0 {
		t.Fatal(result, err)
	}
	account, err := s.PortalAllSubscriptions(user.ID, "get")
	if err != nil || account == "" || account == old {
		t.Fatal("account and single capabilities not distinct", err)
	}
	assignAllocation(t, s, user.ID, p)
	if _, ids, err := s.ResolvePortalImport(importToken(old)); err != nil || len(ids) != 1 || ids[0] != first.ID {
		t.Fatal("old link scope changed", ids, err)
	}
	if _, ids, err := s.ResolvePortalImport(importToken(account)); err != nil || len(ids) != 2 {
		t.Fatal("new account link missing purchase", ids, err)
	}
	result, err = s.PreparePortalAccountImports(context.Background())
	if err != nil || result.Created != 0 || result.Existing != 1 {
		t.Fatal(result, err)
	}
	if current, err := s.PortalAllSubscriptions(user.ID, "get"); err != nil || current != account {
		t.Fatal("replay rotated account link", err)
	}
}

func TestPrepareAccountImportsIncludesDisabledEmptyAccounts(t *testing.T) {
	s, _, user, _ := allocationFixture(t, 1, 1)
	audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", user.ID)
	result, err := s.PreparePortalAccountImports(context.Background())
	if err != nil || result.Created != 1 {
		t.Fatal(result, err)
	}
	if _, err = s.PortalAllSubscriptions(user.ID, "get"); !errors.Is(err, ErrEntitlement) {
		t.Fatal("disabled user allowed to obtain capability", err)
	}
	audienceExec(t, s, "UPDATE portal_users SET enabled=1 WHERE id=?", user.ID)
	link, err := s.PortalAllSubscriptions(user.ID, "get")
	if err != nil || link == "" {
		t.Fatal(err)
	}
	if _, ids, err := s.ResolvePortalImport(importToken(link)); err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", user.ID)
	if _, _, err := s.ResolvePortalImport(importToken(link)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("preparation bypassed disabled owner check", err)
	}
}

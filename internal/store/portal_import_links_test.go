package store

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func importToken(link string) string { return link[strings.LastIndex(link, "/")+1:] }

func TestPortalAllSubscriptionsPrepareAndRotate(t *testing.T) {
	s, _, user, template, node, firstPassword := portalFixture(t, 4, nil)
	first := user.Subscriptions[0]
	second := grantAnother(t, s, user.ID, template, "combined-second")
	// GET does not prepare an account capability or mutate child credentials.
	if link, err := s.PortalAllSubscriptions(user.ID, "get"); err != nil || link != "" {
		t.Fatal("partial link", err)
	}
	link, err := s.PortalAllSubscriptions(user.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	owner, ids, err := s.ResolvePortalImport(importToken(link))
	if err != nil || owner != user.ID || len(ids) != 2 {
		t.Fatal("combined identity", err)
	}
	secondPassword, err := s.PortalCredential(user.ID, second.ID, node.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	firstLease := acquire(t, s, node, firstPassword, "a")
	secondLease := acquire(t, s, node, secondPassword, "b")
	session := nativeSubscriptionSession(t, s, user.ID, first.ID)
	nativePassword, err := s.PortalCredential(user.ID, first.ID, node.ID, session)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"get", "create"} {
		if same, err := s.PortalAllSubscriptions(user.ID, action); err != nil || same != link {
			t.Fatal("non-idempotent preparation", err)
		}
	}
	if same, err := s.PortalCredential(user.ID, first.ID, node.ID, "subscription"); err != nil || same != firstPassword {
		t.Fatal("preparation rotated an existing credential", err)
	}
	rotated, err := s.PortalAllSubscriptions(user.ID, "rotate")
	if err != nil || rotated == link {
		t.Fatal("rotation failed", err)
	}
	if _, _, err = s.ResolvePortalImport(importToken(link)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old envelope accepted")
	}
	for _, password := range []string{firstPassword, secondPassword} {
		if _, err = s.AcquireLease(node.ID, strings.Repeat("c", 32), password); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("old node credential accepted", err)
		}
	}
	for _, lease := range []model.ConnectionLease{firstLease, secondLease} {
		if _, err = s.RenewLease(node.ID, lease.ID, model.LeaseUsage{}); !errors.Is(err, ErrEntitlement) {
			t.Fatal("live connection survived rotation", err)
		}
	}
	if same, err := s.PortalCredential(user.ID, first.ID, node.ID, session); err != nil || same != nativePassword {
		t.Fatal("native credential changed", err)
	}
	if _, err = s.PortalSubscription(user.ID, second.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	if _, ids, err = s.ResolvePortalImport(importToken(rotated)); err != nil || len(ids) != 2 {
		t.Fatal("single rotation changed the independent account capability", err)
	}
}

func TestPortalAllSubscriptionsConcurrentPrepareAndRevoke(t *testing.T) {
	s, _, user, template, _, _ := portalFixture(t, 2, nil)
	grantAnother(t, s, user.ID, template, "concurrent-second")
	var group sync.WaitGroup
	results, failures := make(chan string, 6), make(chan error, 6)
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			link, err := s.PortalAllSubscriptions(user.ID, "create")
			results <- link
			failures <- err
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := ""
	for link := range results {
		if want != "" && want != link {
			t.Fatal("parallel preparation returned obsolete link")
		}
		want = link
	}
	if _, err := s.PortalAllSubscriptions(user.ID, "revoke"); err != nil {
		t.Fatal(err)
	}
	if link, err := s.PortalAllSubscriptions(user.ID, "get"); err != nil || link != "" {
		t.Fatal("GET resurrected revoked link", err)
	}
	if _, _, err := s.ResolvePortalImport(importToken(want)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked bundle accepted")
	}
	if link, err := s.PortalAllSubscriptions(user.ID, "create"); err != nil || link == want || link == "" {
		t.Fatal("explicit new copy failed", err)
	}
}

func TestPortalImportEnvelopeRejectsForeignDuplicateAndOversizedTokens(t *testing.T) {
	s, _, user, template, _, _ := portalFixture(t, 2, nil)
	if _, err := s.PortalSubscription(user.ID, "", "create"); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty single-subscription identity broadened access", err)
	}
	link, err := s.PortalAllSubscriptions(user.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.RegisterPortal("other-import@example.com", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	foreign := grantAnother(t, s, other.ID, template, "foreign")
	foreignLink, err := s.PortalSubscription(other.ID, foreign.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	token := importToken(link)
	for _, value := range []string{"", token + ".", token + "." + token, token + "." + importToken(foreignLink), strings.Repeat(token+".", 101)} {
		if _, _, err = s.ResolvePortalImport(value); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("invalid envelope accepted")
		}
	}
	if _, err = s.PortalSubscription(user.ID, foreign.ID, "rotate"); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign rotation accepted", err)
	}
	if _, _, err = s.ResolvePortalImport(token); err != nil {
		t.Fatal("failed operation changed own link", err)
	}
}

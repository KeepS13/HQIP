package store

import (
	"errors"
	"testing"

	"github.com/KeepS13/HQIP/internal/secure"
)

func TestAdminSessionsAreBoundToAuthenticatedPassword(t *testing.T) {
	s, _ := fixture(t)
	if err := s.InitAdmin("admin", "first-admin-password"); err != nil {
		t.Fatal(err)
	}
	_, firstHash, _, err := s.Admin()
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := s.CreateSession(firstHash)
	if err != nil {
		t.Fatal(err)
	}
	current, csrf, err := s.ChangePasswordAndCreateSession(firstHash, "second-admin-password")
	if err != nil || current == "" || csrf == "" {
		t.Fatalf("atomic password change: %v", err)
	}
	if _, err = s.Session(old); err == nil {
		t.Fatal("old session survived password change")
	}
	if value, err := s.Session(current); err != nil || value != csrf {
		t.Fatal("replacement session is unavailable", err)
	}
	for _, stale := range []string{"", firstHash} {
		if token, _, e := s.CreateSession(stale); !errors.Is(e, ErrUnauthorized) || token != "" {
			t.Fatalf("stale authentication created session: %v", e)
		}
		if token, _, e := s.ChangePasswordAndCreateSession(stale, "third-admin-password"); !errors.Is(e, ErrUnauthorized) || token != "" {
			t.Fatalf("stale authentication changed password: %v", e)
		}
	}
	_, hash, mustChange, err := s.Admin()
	if err != nil || mustChange || !secure.CheckPassword(hash, "second-admin-password") {
		t.Fatal("failed stale mutation changed the current password", err)
	}
	if err = s.ChangePassword("reset-admin-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(current); err == nil {
		t.Fatal("reset left an earlier session valid")
	}
	if _, _, err = s.CreateSession(hash); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("pre-reset authentication still accepted: %v", err)
	}
}

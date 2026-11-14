package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func nativeRefreshFixture(t *testing.T) (*Store, *time.Time, model.PortalTokens) {
	t.Helper()
	s, now := fixture(t)
	u, err := s.RegisterPortal("refresh-recovery@example.com", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tokens, _, err := newPortalSession(tx, u.ID, "native", "desktop", "persistent-installation", *now)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, now, tokens
}
func TestPortalRefreshLostReplyRetriesExactRequestAcrossReopen(t *testing.T) {
	s, _, original := nativeRefreshFixture(t)
	request := strings.Repeat("a", 48)
	fresh, err := s.PortalRefreshRequest(original.RefreshToken, request)
	if err != nil {
		t.Fatal(err)
	}
	var duplicate model.PortalTokens
	reopened := &Store{DB: s.DB, Vault: s.Vault, Now: s.Now, BaseURL: s.BaseURL}
	duplicate, err = reopened.PortalRefreshRequest(original.RefreshToken, request)
	if err != nil || duplicate.AccessToken != fresh.AccessToken || duplicate.RefreshToken != fresh.RefreshToken {
		t.Fatalf("lost rotation: %v", err)
	}
	if _, _, _, err = s.PortalIdentity(fresh.AccessToken, "native"); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PortalRefreshRequest(original.RefreshToken, strings.Repeat("b", 48)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("different request replay accepted")
	}
	if _, _, _, err = s.PortalIdentity(fresh.AccessToken, "native"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("replay no longer revokes session")
	}
	if _, err = s.PortalRefreshRequest(original.RefreshToken, request); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("receipt resurrected revoked session")
	}
}
func TestPortalRefreshReceiptExpiresAndCannotRollTokensBack(t *testing.T) {
	for _, mode := range []string{"expired", "advanced"} {
		t.Run(mode, func(t *testing.T) {
			s, now, original := nativeRefreshFixture(t)
			request := strings.Repeat("a", 48)
			fresh, err := s.PortalRefreshRequest(original.RefreshToken, request)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "expired" {
				*now = now.Add(11 * time.Minute)
			} else {
				if _, err = s.PortalRefreshRequest(fresh.RefreshToken, strings.Repeat("b", 48)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.PortalRefreshRequest(original.RefreshToken, request); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("stale receipt accepted")
			}
		})
	}
}
func TestPortalDatabaseFailureIsNotInvalidLogin(t *testing.T) {
	s, _, token := nativeRefreshFixture(t)
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.PortalIdentity(token.AccessToken, "native"); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("identity error misclassified: %v", err)
	}
	if _, err := s.PortalRefresh(token.RefreshToken); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("refresh error misclassified: %v", err)
	}
}

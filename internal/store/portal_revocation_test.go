package store

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestPortalAccountChangesConsumeApprovedCodes(t *testing.T) {
	for _, action := range []string{"admin-reset", "self-password", "disable-reenable"} {
		t.Run(action, func(t *testing.T) {
			s, _ := fixture(t)
			const password = "old-password-long-enough"
			u, err := s.RegisterPortal("code@example.com", password)
			if err != nil {
				t.Fatal(err)
			}
			browser, _, err := s.PortalLogin(u.Email, password)
			if err != nil {
				t.Fatal(err)
			}
			verifier := strings.Repeat("v", 43)
			sum := sha256.Sum256([]byte(verifier))
			redirect := "http://127.0.0.1:41234/callback"
			id, _, err := s.BeginPortalAuthorization(base64.RawURLEncoding.EncodeToString(sum[:]), redirect, strings.Repeat("s", 32), "desktop", "installation-revoke")
			if err != nil {
				t.Fatal(err)
			}
			callback, err := s.ApprovePortalAuthorization(id, u.ID, browser.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := url.Parse(callback)
			if action == "disable-reenable" {
				if err = s.SetPortalEnabled(u.ID, false); err == nil {
					err = s.SetPortalEnabled(u.ID, true)
				}
			} else {
				err = s.PortalPassword(u.ID, password, "new-password-long-enough", action == "admin-reset")
			}
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if _, err = s.ExchangePortalCode(parsed.Query().Get("code"), verifier, redirect, "installation-revoke"); !errors.Is(err, ErrUnauthorized) {
					t.Fatalf("old approved code survived %s: %v", action, err)
				}
			}
			var count int
			if err = s.DB.QueryRow("SELECT COUNT(*) FROM portal_sessions WHERE user_id=? AND kind='native'", u.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("replayed code created native session: %d, %v", count, err)
			}
		})
	}
}

func TestPortalAuthorizationRejectsWrongSessionKindOrOwner(t *testing.T) {
	s, _ := fixture(t)
	u, err := s.RegisterPortal("owner@example.com", "password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.RegisterPortal("other@example.com", "password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := s.PortalLogin(other.Email, "password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	native, _, err := newPortalSession(tx, u.ID, "native", "test", "test-installation", s.Now())
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.BeginPortalAuthorization(strings.Repeat("v", 43), "http://127.0.0.1:41234/callback", strings.Repeat("s", 32), "desktop", "installation-revoke")
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"", foreign.SessionID, native.SessionID} {
		if _, err = s.ApprovePortalAuthorization(id, u.ID, session); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("wrong session allowed approval: %v", err)
		}
	}
}

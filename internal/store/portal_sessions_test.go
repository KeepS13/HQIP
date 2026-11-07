package store

import (
	"fmt"
	"testing"
	"time"
)

func addListedPortalSession(t *testing.T, s *Store, user, id string, created, last, expires time.Time, revoked bool) {
	t.Helper()
	_, err := s.DB.Exec(`INSERT INTO portal_sessions
		(id,user_id,kind,client_name,access_hash,refresh_hash,csrf,access_expires,expires,created_at,last_seen,revoked)
		VALUES(?,?,'native',?,?,?,?,?,?,?,?,?)`, id, user, id, "access-"+id, "refresh-"+id, "csrf-"+id,
		created.Unix(), expires.Unix(), created.Unix(), last.Unix(), revoked)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPortalSessionsActiveBeforeHistoryLimit(t *testing.T) {
	s, now := fixture(t)
	user, err := s.RegisterPortal("session-order@example.com", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	// More than a full page of newer history must not evict old active devices.
	for i := range 110 {
		at := now.Add(-time.Duration(i) * time.Minute)
		expires := now.Add(time.Hour)
		if i%2 == 0 {
			expires = *now // Expiry is exclusive, including the exact boundary.
		}
		addListedPortalSession(t, s, user.ID, fmt.Sprintf("history-%03d", i), at, at, expires, i%2 != 0)
	}
	created := now.Add(-48 * time.Hour)
	addListedPortalSession(t, s, user.ID, "current", created, now.Add(-24*time.Hour), now.Add(time.Hour), false)
	addListedPortalSession(t, s, user.ID, "active-older", created.Add(time.Hour), now.Add(-12*time.Hour), now.Add(time.Hour), false)
	addListedPortalSession(t, s, user.ID, "active-recent", created, now.Add(-6*time.Hour), now.Add(time.Hour), false)
	rows, err := s.PortalSessions(user.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 100 {
		t.Fatalf("list limit: got %d, want 100", len(rows))
	}
	for i, id := range []string{"current", "active-recent", "active-older", "history-000", "history-001"} {
		if rows[i].ID != id {
			t.Fatalf("row %d = %q, want %q", i, rows[i].ID, id)
		}
	}
	if !rows[0].Current {
		t.Fatal("valid current session lost its current flag")
	}
	// Admin view has no current browser and still orders active devices by activity.
	rows, err = s.PortalSessions(user.ID, "")
	if err != nil || rows[0].ID != "active-recent" || rows[0].Current {
		t.Fatalf("admin session order: rows=%+v, err=%v", rows, err)
	}
}

func TestPortalSessionsRevokedAndExpiredCurrentStayInHistory(t *testing.T) {
	s, now := fixture(t)
	user, err := s.RegisterPortal("session-revoke@example.com", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	addListedPortalSession(t, s, user.ID, "active", now.Add(-time.Hour), now.Add(-time.Hour), now.Add(time.Hour), false)
	addListedPortalSession(t, s, user.ID, "current", *now, *now, now.Add(time.Hour), false)
	addListedPortalSession(t, s, user.ID, "expired", now.Add(time.Second), now.Add(time.Second), *now, false)
	if err = s.RevokePortalSession(user.ID, "current"); err != nil {
		t.Fatal(err)
	}
	for _, current := range []string{"current", "expired"} {
		rows, err := s.PortalSessions(user.ID, current)
		if err != nil {
			t.Fatal(err)
		}
		for i, id := range []string{"active", "expired", "current"} {
			if rows[i].ID != id || rows[i].Current {
				t.Fatalf("current=%q row %d: %+v, want %q without current flag", current, i, rows[i], id)
			}
		}
		if !rows[2].Revoked {
			t.Fatal("revoked session must remain marked revoked")
		}
	}
}

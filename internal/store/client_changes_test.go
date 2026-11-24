package store

import (
	"context"
	"testing"
	"time"
)

func TestClientChangeTokenTracksBusinessNotTraffic(t *testing.T) {
	s, now, user, _, node, _ := portalFixture(t, 2, nil)
	if _, err := s.Rules(); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		token, err := s.ClientChangeToken(context.Background(), user.ID)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	first := read()
	if len(first) != 64 || read() != first {
		t.Fatal("unstable business token")
	}
	if _, err := s.DB.Exec("UPDATE portal_usage SET upload=upload+100 WHERE subscription_id=?", user.Subscriptions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE nodes SET last_seen=last_seen+1 WHERE id=?", node.ID); err != nil {
		t.Fatal(err)
	}
	if read() != first {
		t.Fatal("traffic/heartbeat caused a catalog import")
	}
	if _, err := s.DB.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	second := read()
	if second == first {
		t.Fatal("website business change was not visible")
	}
	if _, err := s.DB.Exec("UPDATE nodes SET lifecycle='retired' WHERE id=?", node.ID); err != nil {
		t.Fatal(err)
	}
	third := read()
	if third == second {
		t.Fatal("assigned node configuration was not visible")
	}
	*now = now.Add(48 * time.Hour)
	if read() == third {
		t.Fatal("expiry without a database write was not visible")
	}
}

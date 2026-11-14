package store

import (
	"strings"
	"testing"
	"time"
)

func TestMaintenanceBatchesAndKeepsLiveSessions(t *testing.T) {
	s, now, token := nativeRefreshFixture(t)
	node := newNode(t, s, "retention")
	if _, err := s.PortalRefreshRequest(token.RefreshToken, strings.Repeat("b", 48)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE portal_spent_refresh SET expires=?", now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < maintenanceBatchSize+7; i++ {
		if _, err = tx.Exec("INSERT INTO events(node_id,stage,message,created_at) VALUES(?,'info','old',?)", node.ID, now.AddDate(0, 0, -31).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.Maintenance(); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM events WHERE message='old'").Scan(&remaining); err != nil || remaining != 7 {
		t.Fatalf("batch not bounded: %d, %v", remaining, err)
	}
	for table, want := range map[string]int{"portal_sessions": 1, "portal_spent_refresh": 0, "portal_refresh_receipts": 1} {
		var got int
		if err = s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: %d != %d (%v)", table, got, want, err)
		}
	}
	// Refresh lifetime + retention: the exact expiry is the authoritative cutoff.
	if _, err = s.DB.Exec("UPDATE portal_sessions SET expires=?", now.AddDate(0, 0, -31).Unix()); err != nil {
		t.Fatal(err)
	}
	if err = s.Maintenance(); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM portal_sessions").Scan(&n)
	if n != 1 {
		t.Fatal("session with unexpired refresh receipt removed")
	}
	*now = now.Add(time.Hour)
	if err = s.Maintenance(); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM portal_sessions").Scan(&n)
	if n != 0 {
		t.Fatal("expired session not retired")
	}
	if err = s.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceFailureDoesNotUndoOtherTables(t *testing.T) {
	s, now := sqliteFixture(t)
	node := newNode(t, s, "cleanup")
	for _, q := range []string{
		"INSERT INTO events(node_id,stage,message,created_at) VALUES('" + node.ID + "','info','old',1)",
		"INSERT INTO audit(at,action,subject) VALUES(1,'old','test')",
		"CREATE TRIGGER fail_events BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT,'blocked'); END",
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	*now = now.AddDate(2, 0, 0)
	err := s.Maintenance()
	if err == nil || !strings.Contains(err.Error(), "events batch failed") {
		t.Fatalf("missing failure: %v", err)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM audit").Scan(&n)
	if n != 0 {
		t.Fatal("blocked events rolled back audit cleanup")
	}
	s.DB.Exec("DROP TRIGGER fail_events")
	if err = s.Maintenance(); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 0 {
		t.Fatal("failed batch never recovered")
	}
}

func TestFinishJobReportsFailureAndRetryIsIdempotent(t *testing.T) {
	s, _ := sqliteFixture(t)
	newNode(t, s, "completion")
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER fail_finish BEFORE UPDATE ON jobs WHEN NEW.state IN ('done','error') BEGIN SELECT RAISE(ABORT,'blocked'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(j, true); err == nil {
		t.Fatal("completion error swallowed")
	}
	s.DB.Exec("DROP TRIGGER fail_finish")
	for range 2 {
		if err = s.FinishJob(j, true); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	s.DB.QueryRow("SELECT state FROM jobs WHERE id=?", j.ID).Scan(&state)
	if state != "done" {
		t.Fatal(state)
	}
}

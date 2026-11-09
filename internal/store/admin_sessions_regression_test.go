package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/secure"
)

func TestAdminPasswordChangeCannotOverwriteConcurrentReset(t *testing.T) {
	s, _ := fixture(t)
	if err := s.InitAdmin("admin", "original-admin-password"); err != nil {
		t.Fatal(err)
	}
	_, originalHash, _, err := s.Admin()
	if err != nil {
		t.Fatal(err)
	}
	now := s.Now()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.Now = func() time.Time {
		close(entered)
		<-release
		return now
	}
	type outcome struct {
		token, csrf string
		err         error
	}
	finished := make(chan outcome, 1)
	go func() {
		token, csrf, err := s.ChangePasswordAndCreateSession(originalHash, "stale-request-password")
		finished <- outcome{token, csrf, err}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("password change did not reach the session barrier")
	}
	if err := s.ChangePassword("administrator-reset-password"); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case result := <-finished:
		if !errors.Is(result.err, ErrUnauthorized) || result.token != "" || result.csrf != "" {
			t.Fatalf("stale password change was accepted: %v", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("password change remained blocked")
	}
	_, hash, _, err := s.Admin()
	if err != nil || !secure.CheckPassword(hash, "administrator-reset-password") {
		t.Fatal("concurrent reset was overwritten", err)
	}
	var sessions int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("stale password change created a session: count=%d err=%v", sessions, err)
	}
}

func TestAdminPasswordChangeRollsBackWhenSessionInsertFails(t *testing.T) {
	s, _ := fixture(t)
	if err := s.InitAdmin("admin", "original-admin-password"); err != nil {
		t.Fatal(err)
	}
	_, originalHash, originalMustChange, err := s.Admin()
	if err != nil {
		t.Fatal(err)
	}
	oldToken, oldCSRF, err := s.CreateSession(originalHash)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{`CREATE TRIGGER reject_admin_session BEFORE INSERT ON sessions
 BEGIN SELECT RAISE(ABORT, 'injected session insert failure'); END`}
	if s.Postgres() {
		statements = []string{
			`CREATE FUNCTION reject_admin_session_insert() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'injected session insert failure'; RETURN NULL; END $$`,
			`CREATE TRIGGER reject_admin_session BEFORE INSERT ON sessions
 FOR EACH ROW EXECUTE FUNCTION reject_admin_session_insert()`,
		}
	}
	for _, statement := range statements {
		if _, err := s.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if token, csrf, err := s.ChangePasswordAndCreateSession(originalHash, "replacement-admin-password"); err == nil || token != "" || csrf != "" {
		t.Fatalf("failed insert returned a replacement session: %v", err)
	}
	_, hash, mustChange, err := s.Admin()
	if err != nil || hash != originalHash || mustChange != originalMustChange {
		t.Fatal("failed session insert committed the password change", err)
	}
	if csrf, err := s.Session(oldToken); err != nil || csrf != oldCSRF {
		t.Fatal("failed session insert invalidated the original session", err)
	}
	var sessions int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("unexpected sessions after rollback: count=%d err=%v", sessions, err)
	}
}

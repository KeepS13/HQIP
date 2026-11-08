package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func requireUnread(t *testing.T, s *Store, want int) {
	t.Helper()
	summary, err := s.AlertUnreadSummary()
	if err != nil || summary.Unread != want {
		t.Fatalf("unread=%d, want %d: %v", summary.Unread, want, err)
	}
}

func TestAlertReadIndependentFromAcknowledgementAndNewChanges(t *testing.T) {
	s, now := fixture(t)
	c := model.NotificationCondition{Key: "capacity:read-test", Kind: "server.traffic", Category: "capacity", Title: "流量提醒", Message: "达到80%", Stage: 1}
	alertEval(t, s, c)
	a := alertList(t, s)[0]
	requireUnread(t, s, 1)
	if err := s.MarkAlertRead(a.ID, "admin", a.Revision); err != nil {
		t.Fatal(err)
	}
	requireUnread(t, s, 0)
	read, err := s.Alert(a.ID)
	if err != nil || read.Acknowledged || read.Revision != a.Revision {
		t.Fatalf("reading modified confirmation or revision: %+v %v", read, err)
	}
	// Repeat polling, notes and manual confirmation do not create unread work.
	if err = s.MarkAlertRead(a.ID, "admin", a.Revision); err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateAlert(a.ID, "admin", model.AlertUpdate{ExpectedRevision: a.Revision, Acknowledge: true, Note: "已处理"})
	if err != nil {
		t.Fatal(err)
	}
	requireUnread(t, s, 0)
	if err = s.MarkAlertRead(a.ID, "admin", updated.Revision); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM alert_activity WHERE alert_id=? AND kind='read'", a.ID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("duplicate read receipts: %d %v", receipts, err)
	}
	// An escalation in the same second must be unread and stale details cannot
	// consume it. Activity IDs, rather than timestamps, establish the order.
	c.Stage = 2
	c.Message = "额度已满"
	alertEval(t, s, c)
	requireUnread(t, s, 1)
	if err = s.MarkAlertRead(a.ID, "admin", updated.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale read accepted: %v", err)
	}
	requireUnread(t, s, 1)
	latest, err := s.Alert(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkAlertRead(a.ID, "admin", latest.Revision); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	alertEval(t, s)
	requireUnread(t, s, 1)
	ended, err := s.Alert(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkAlertRead(a.ID, "admin", ended.Revision); err != nil {
		t.Fatal(err)
	}
	requireUnread(t, s, 0)
	alertEval(t, s, c)
	requireUnread(t, s, 1)
}

func TestAlertReadPersistsAndPendingIsNotReadable(t *testing.T) {
	s, _ := fixture(t)
	c := model.NotificationCondition{Key: "health:pending", Kind: "node.health", Category: "system", Title: "节点异常", Stage: 1, DelaySeconds: 60}
	alertEval(t, s, c)
	requireUnread(t, s, 0)
	var pendingID string
	if err := s.DB.QueryRow("SELECT id FROM alerts WHERE key=?", c.Key).Scan(&pendingID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAlertRead(pendingID, "admin", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending alert can be read: %v", err)
	}
	if err := s.MarkAlertRead("missing", "admin", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing alert: %v", err)
	}
	c.DelaySeconds = 0
	alertEval(t, s, c)
	a := alertList(t, s)[0]
	if err := s.MarkAlertRead(a.ID, "admin", a.Revision); err != nil {
		t.Fatal(err)
	}
	reopened, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	requireUnread(t, reopened, 0)
}

func TestAlertConcurrentReadReceiptsAreIdempotent(t *testing.T) {
	s, _ := fixture(t)
	alertEval(t, s, model.NotificationCondition{Key: "read-concurrency", Category: "system", Kind: "node.health", Title: "异常", Stage: 1})
	a := alertList(t, s)[0]
	var workers sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		workers.Go(func() {
			failures <- s.MarkAlertRead(a.ID, "admin", a.Revision)
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var receipts int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM alert_activity WHERE alert_id=? AND kind='read'", a.ID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("concurrent reads wrote %d receipts: %v", receipts, err)
	}
	requireUnread(t, s, 0)
}

func TestPostgresAlertReadCannotConsumeUncommittedEscalation(t *testing.T) {
	s, _ := fixture(t)
	if !s.Postgres() {
		t.Skip("requires PostgreSQL concurrent snapshots")
	}
	alertEval(t, s, model.NotificationCondition{Key: "read-overlap", Category: "capacity", Kind: "server.traffic", Title: "流量提醒", Stage: 1})
	a := alertList(t, s)[0]
	if err := s.MarkAlertRead(a.ID, "admin", a.Revision); err != nil {
		t.Fatal(err)
	}
	requireUnread(t, s, 0)
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	escalated, err := readAlert(tx.QueryRow("SELECT "+alertColumns+" FROM alerts WHERE id=?", a.ID))
	if err != nil {
		t.Fatal(err)
	}
	escalated.Condition.Stage++
	escalated.Revision++
	if err = saveAlert(tx, escalated); err != nil {
		t.Fatal(err)
	}
	if err = alertActivity(tx, a.ID, "escalated", "额度已满", "", s.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	// The new activity already owns an ID, but its transaction is not visible.
	readDone := make(chan error, 1)
	go func() { readDone <- s.MarkAlertRead(a.ID, "admin", a.Revision) }()
	select {
	case err = <-readDone:
		t.Fatalf("read did not wait for the evaluator's row lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-readDone:
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("stale detail consumed escalation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not finish after evaluator commit")
	}
	requireUnread(t, s, 1)
}

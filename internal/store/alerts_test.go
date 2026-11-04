package store

import (
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
	"strings"
	"testing"
	"time"
)

func alertList(t *testing.T, s *Store) []model.Alert {
	t.Helper()
	v, e := s.ListAlerts(1, 100, "", "", "", "", false)
	if e != nil {
		t.Fatal(e)
	}
	return v.Items
}
func alertEval(t *testing.T, s *Store, c ...model.NotificationCondition) {
	t.Helper()
	if e := s.EvaluateAlerts(c); e != nil {
		t.Fatal(e)
	}
}
func TestAlertLifecycleUnknownAcknowledgementEscalationAndRetention(t *testing.T) {
	s, now := fixture(t)
	c := model.NotificationCondition{Key: "node.health:x", Kind: "node.health", Category: "system", Title: "节点异常", Message: "内核异常", Stage: 1, Continuous: true, DelaySeconds: 60, RecoverySeconds: 30}
	alertEval(t, s, c)
	if len(alertList(t, s)) != 0 {
		t.Fatal("premature alert")
	}
	*now = now.Add(60 * time.Second)
	alertEval(t, s, c)
	a := alertList(t, s)[0]
	updated, e := s.UpdateAlert(a.ID, "admin", model.AlertUpdate{ExpectedRevision: a.Revision, Acknowledge: true, Note: "已开始检查"})
	if e != nil || !updated.Acknowledged {
		t.Fatal(e)
	}
	if _, e = s.UpdateAlert(a.ID, "admin", model.AlertUpdate{ExpectedRevision: a.Revision, Note: "stale"}); !errors.Is(e, ErrConflict) {
		t.Fatal("missing revision conflict", e)
	}
	unknown := c
	unknown.Unknown = true
	alertEval(t, s, unknown)
	a = alertList(t, s)[0]
	if a.Status != "unknown" || !a.Acknowledged {
		t.Fatal(a)
	}
	*now = now.Add(30 * time.Second)
	alertEval(t, s, c)
	if alertList(t, s)[0].Status != "unknown" {
		t.Fatal("delay not reset after unknown")
	}
	*now = now.Add(60 * time.Second)
	alertEval(t, s, c)
	if alertList(t, s)[0].Status != "active" {
		t.Fatal("continuous evidence did not mature")
	}
	c.Stage = 2
	alertEval(t, s, c)
	if alertList(t, s)[0].Acknowledged {
		t.Fatal("escalation remained acknowledged")
	}
	alertEval(t, s)
	*now = now.Add(29 * time.Second)
	alertEval(t, s)
	if alertList(t, s)[0].Status != "active" {
		t.Fatal("premature recovery")
	}
	*now = now.Add(time.Second)
	alertEval(t, s)
	if alertList(t, s)[0].Status != "resolved" {
		t.Fatal("recovery missing")
	}
	alertEval(t, s, c)
	*now = now.Add(60 * time.Second)
	alertEval(t, s, c)
	if len(alertList(t, s)) != 1 || alertList(t, s)[0].Acknowledged {
		t.Fatal("recurrence did not reuse event/reset acknowledgement")
	}
	*now = now.AddDate(0, 0, 91)
	alertEval(t, s, c)
	if len(alertList(t, s)) != 1 {
		t.Fatal("active alert pruned")
	}
	c.Removed = true
	alertEval(t, s, c)
	if alertList(t, s)[0].Status != "ended" {
		t.Fatal("deletion masqueraded as recovery")
	}
	*now = now.AddDate(0, 0, 91)
	alertEval(t, s)
	if len(alertList(t, s)) != 0 {
		t.Fatal("old ended alert not pruned")
	}
}
func TestAlertQuotaCycleAndReopenPersistAcrossRestart(t *testing.T) {
	s, now := fixture(t)
	c := model.NotificationCondition{Key: "node.month:x:2026-10", Kind: "node.month", Category: "capacity", Title: "月容量", Message: "80%", Stage: 80}
	alertEval(t, s, c)
	a := alertList(t, s)[0]
	_, err := s.UpdateAlert(a.ID, "admin", model.AlertUpdate{ExpectedRevision: a.Revision, Acknowledge: true})
	if err != nil {
		t.Fatal(err)
	}
	// Reopening a connection exercises schema migration without resetting state.
	other, err := Open(s.DataDir, s.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	other.Now = s.Now
	defer other.Close()
	alertEval(t, other, c)
	if !alertList(t, other)[0].Acknowledged {
		t.Fatal("restart lost acknowledgement")
	}
	*now = now.Add(30 * time.Second)
	alertEval(t, s)
	if alertList(t, s)[0].Status != "ended" {
		t.Fatal("quota removal must end not recover")
	}
	c.Key = "node.month:x:2026-11"
	alertEval(t, s, c)
	if len(alertList(t, s)) != 2 {
		t.Fatal("month not isolated")
	}
}
func TestUserPasswordsSixUnicodeCharacters(t *testing.T) {
	s, _ := fixture(t)
	for i, password := range []string{"12345", "😀😀😀😀😀", "123456", "六个中文字符", "😀😀😀😀😀😀", strings.Repeat("a", 1025)} {
		_, err := s.RegisterPortal(strings.Repeat("a", i+1)+"@example.com", password)
		valid := i >= 2 && i <= 4
		if (err == nil) != valid {
			t.Fatalf("case %d valid=%v error=%v", i, valid, err)
		}
	}
	u, e := s.RegisterPortal("change@example.com", "123456")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PortalPassword(u.ID, "123456", "短短短短短", false); e == nil {
		t.Fatal("short change allowed")
	}
	if e = s.PortalPassword(u.ID, "123456", "abcdef", false); e != nil {
		t.Fatal(e)
	}
	if e = s.PortalPassword(u.ID, "", "654321", true); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.PortalLogin("change@example.com", "654321"); e != nil {
		t.Fatal("reset password cannot login", e)
	}
}

func TestJobAlertsPersistUntilSameKindSuccess(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "failed-install")
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.FinishJob(j, false)
	alertEval(t, s)
	items := alertList(t, s)
	if len(items) != 1 || items[0].Status != "active" {
		t.Fatal(items)
	}
	notificationExec(t, s, "INSERT INTO jobs(node_id,kind,state,created_at) VALUES(?,'verify','done',?)", n.ID, now.Unix())
	alertEval(t, s)
	if alertList(t, s)[0].Status != "active" {
		t.Fatal("unrelated successful job hid installation failure")
	}
	notificationExec(t, s, "INSERT INTO jobs(node_id,kind,state,created_at) VALUES(?,'install','done',?)", n.ID, now.Unix())
	alertEval(t, s)
	if alertList(t, s)[0].Status != "ended" {
		t.Fatal("completed operation did not end previous failure")
	}
}

func TestLegacyRobotMigrationDisablesSecretsAndRetainsOnlyBusinessHistory(t *testing.T) {
	s, now := fixture(t)
	notificationExec(t, s, "DELETE FROM metadata WHERE key='alerts_migrated'")
	notificationExec(t, s, `CREATE TABLE feishu_settings(enabled INTEGER,webhook TEXT,secret TEXT);INSERT INTO feishu_settings VALUES(1,'encrypted-webhook','encrypted-secret');
 CREATE TABLE notifications(id TEXT,category TEXT,level TEXT,title TEXT,message TEXT,detail_path TEXT,created_at INTEGER,status TEXT,claim TEXT,claim_until INTEGER,next_attempt INTEGER,last_error TEXT);`)
	for _, category := range []string{"system", "test"} {
		notificationExec(t, s, "INSERT INTO notifications VALUES(?,?,'danger','历史预警','原因','/admin/nodes/node',?,'pending','claim',1,1,'')", category, category, now.Unix())
	}
	if e := EnsureNotificationSchema(s.DB); e != nil {
		t.Fatal(e)
	}
	var enabled int
	var webhook, secret string
	if e := s.DB.QueryRow("SELECT enabled,webhook,secret FROM feishu_settings").Scan(&enabled, &webhook, &secret); e != nil || enabled != 0 || webhook != "" || secret != "" {
		t.Fatal("robot credentials survived", e)
	}
	items := alertList(t, s)
	if len(items) != 1 || items[0].Status != "ended" || items[0].Condition.DetailPath != "/nodes/node" {
		t.Fatal(items)
	}
	if e := EnsureNotificationSchema(s.DB); e != nil {
		t.Fatal(e)
	}
	if len(alertList(t, s)) != 1 {
		t.Fatal("migration duplicated history")
	}
}

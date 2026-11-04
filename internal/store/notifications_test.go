package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func enableFeishu(t *testing.T, s *Store) model.FeishuSettings {
	t.Helper()
	v, err := s.SaveFeishuSettings(model.FeishuSettingsInput{Enabled: true, Webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/12345678-abcd-1234-abcd-123456789abc", Secret: "test-signing-secret", Categories: model.DefaultNotificationCategories()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func condition(key string, stage int, continuous bool) model.NotificationCondition {
	return model.NotificationCondition{Key: key, Category: "capacity", Kind: "quota", ResourceID: "resource", Title: "容量提醒", Message: "已使用80%", DetailPath: "/admin/servers/resource", Level: "warning", Stage: stage, Continuous: continuous}
}
func evaluate(t *testing.T, s *Store, cs ...model.NotificationCondition) {
	t.Helper()
	if err := s.EvaluateNotifications(cs); err != nil {
		t.Fatal(err)
	}
}
func claim(t *testing.T, s *Store) *model.NotificationBatch {
	t.Helper()
	b, e := s.ClaimNotificationBatch(20)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func delivered(t *testing.T, s *Store, b *model.NotificationBatch) {
	t.Helper()
	if b == nil {
		t.Fatal("expected delivery")
	}
	if e := s.CompleteNotificationBatch(b, true, false, ""); e != nil {
		t.Fatal(e)
	}
}
func notifications(t *testing.T, s *Store) []model.NotificationRecord {
	t.Helper()
	p, e := s.Notifications(1, 100)
	if e != nil {
		t.Fatal(e)
	}
	return p.Items
}

func TestFeishuSettingsEncryptionRevisionAndClearing(t *testing.T) {
	s, _ := fixture(t)
	defaults, e := s.FeishuSettings()
	if e != nil {
		t.Fatal(e)
	}
	if defaults.Enabled || !defaults.Categories.Package || defaults.Revision != 0 {
		t.Fatalf("defaults: %+v", defaults)
	}
	settings := enableFeishu(t, s)
	var webhook, secret string
	if e = s.DB.QueryRow("SELECT webhook,secret FROM feishu_settings").Scan(&webhook, &secret); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(webhook, "12345678") || strings.Contains(secret, "test-signing-secret") {
		t.Fatal("credentials plaintext")
	}
	encoded, _ := json.Marshal(settings)
	if strings.Contains(string(encoded), "test-signing-secret") || strings.Contains(string(encoded), "12345678") {
		t.Fatal("credentials exposed")
	}
	_, e = s.SaveFeishuSettings(model.FeishuSettingsInput{Enabled: true, Categories: settings.Categories, ExpectedRevision: 0})
	if !errors.Is(e, ErrConflict) {
		t.Fatalf("revision: %v", e)
	}
	_, e = s.SaveFeishuSettings(model.FeishuSettingsInput{Enabled: true, ClearCredentials: true, ExpectedRevision: settings.Revision})
	if e == nil {
		t.Fatal("enabled clearing accepted")
	}
	cleared, e := s.SaveFeishuSettings(model.FeishuSettingsInput{ClearCredentials: true, Categories: settings.Categories, ExpectedRevision: settings.Revision})
	if e != nil || cleared.Enabled || cleared.WebhookConfigured || cleared.SecretConfigured {
		t.Fatalf("clear: %+v %v", cleared, e)
	}
	_, e = s.QueueFeishuTest()
	if e == nil {
		t.Fatal("test without credentials")
	}
}

func TestNotificationInitialSummaryDedupAndRestart(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a, b := condition("a:2026-11", 80, false), condition("b:2026-11", 100, false)
	evaluate(t, s, a, b)
	batch := claim(t, s)
	if batch == nil || len(batch.Records) != 1 || batch.Records[0].Category != "summary" || !strings.Contains(batch.Records[0].Message, "2 项") {
		t.Fatalf("summary: %+v", batch)
	}
	delivered(t, s, batch)
	evaluate(t, s, a, b)
	*now = now.Add(time.Minute)
	if claim(t, s) != nil {
		t.Fatal("duplicate summary")
	}
	dir, base := s.DataDir, s.BaseURL
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Now = func() time.Time { return *now }
	evaluate(t, reopened, a, b)
	if claim(t, reopened) != nil {
		t.Fatal("restart duplicated sent threshold")
	}
}

func TestNotificationThresholdHighestPeriodAndStaleCancellation(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	a := condition("quota:2026-11", 80, false)
	evaluate(t, s, a)
	a.Stage = 100
	evaluate(t, s, a)
	batch := claim(t, s)
	if batch == nil || len(batch.Records) != 1 {
		t.Fatalf("crossed threshold %+v", batch)
	}
	var refs string
	if err := s.DB.QueryRow("SELECT refs FROM notifications WHERE id=?", batch.Records[0].ID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refs, "100") {
		t.Fatal(refs)
	}
	delivered(t, s, batch)
	*now = now.Add(time.Minute)
	a.Stage = 80
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("lower threshold replayed")
	}
	a.Key = "quota:2026-12"
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	*now = now.Add(time.Minute)
	a.Key = "quota:new-period"
	evaluate(t, s, a)
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("obsolete threshold delivered")
	}
}

func TestNotificationContinuousDelayUnknownRecoveryAndRepeat(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	a := condition("node.health:n", 1, true)
	a.Category = "system"
	a.DelaySeconds = 60
	a.RecoverySeconds = 30
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("ignored 60s delay")
	}
	*now = now.Add(59 * time.Second)
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("early warning")
	}
	*now = now.Add(time.Second)
	evaluate(t, s, a)
	batch := claim(t, s)
	delivered(t, s, batch)
	*now = now.Add(5 * time.Hour)
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("repeated before6h")
	}
	*now = now.Add(time.Hour)
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	*now = now.Add(time.Minute)
	unknown := a
	unknown.Unknown = true
	evaluate(t, s, unknown)
	*now = now.Add(time.Hour)
	evaluate(t, s, unknown)
	if claim(t, s) != nil {
		t.Fatal("unknown falsely recovered")
	}
	evaluate(t, s)
	*now = now.Add(29 * time.Second)
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("early recovery")
	}
	*now = now.Add(time.Second)
	evaluate(t, s)
	recovery := claim(t, s)
	if recovery == nil || len(recovery.Records) != 1 || recovery.Records[0].Category != "recovery" {
		t.Fatalf("recovery %+v", recovery)
	}
	delivered(t, s, recovery)
	*now = now.Add(time.Hour)
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("duplicate recovery")
	}
	evaluate(t, s, a)
	*now = now.Add(time.Minute)
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
}

func TestNotificationUnknownPausesPendingAndSummaryRefreshes(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a, b := condition("a", 80, false), condition("b", 80, false)
	evaluate(t, s, a, b)
	b.Unknown = true
	evaluate(t, s, a, b)
	if claim(t, s) != nil {
		t.Fatal("unknown summary sent")
	}
	b.Unknown = false
	evaluate(t, s, b)
	batch := claim(t, s)
	if batch == nil || !strings.Contains(batch.Records[0].Message, "1 项") {
		t.Fatalf("stale summary %+v", batch)
	}
	delivered(t, s, batch)
	*now = now.Add(time.Minute)
	evaluate(t, s, b)
	if claim(t, s) != nil {
		t.Fatal("duplicate")
	}
}

func TestNotificationConfigChangeCancelsOldClaimsAndCategoryFiltering(t *testing.T) {
	s, now := fixture(t)
	cfg := enableFeishu(t, s)
	evaluate(t, s, condition("old", 80, false))
	batch := claim(t, s)
	categories := model.DefaultNotificationCategories()
	categories.Capacity = false
	_, err := s.SaveFeishuSettings(model.FeishuSettingsInput{Enabled: true, Categories: categories, ExpectedRevision: cfg.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteNotificationBatch(batch, true, false, ""); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	evaluate(t, s, condition("old", 80, false))
	if claim(t, s) != nil {
		t.Fatal("disabled category queued")
	}
	items := notifications(t, s)
	if len(items) != 1 || items[0].Status != "cancelled" {
		t.Fatalf("old claim %+v", items)
	}
}

func TestNotificationRetryBudgetRateLimitAndManualRetry(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	a := condition("capacity", 80, false)
	evaluate(t, s, a)
	var id string
	for attempt := 1; attempt <= 6; attempt++ {
		batch := claim(t, s)
		if batch == nil {
			t.Fatalf("missing attempt%d", attempt)
		}
		id = batch.Records[0].ID
		if err := s.CompleteNotificationBatch(batch, false, true, "飞书暂时不可用"); err != nil {
			t.Fatal(err)
		}
		if claim(t, s) != nil {
			t.Fatal("rate limit not respected")
		}
		items := notifications(t, s)
		if items[0].Attempts != attempt {
			t.Fatal(items)
		}
		if attempt < 6 {
			if items[0].Status != "retry" || items[0].NextAttempt == nil {
				t.Fatal(items)
			}
			*now = items[0].NextAttempt.Add(time.Second)
		} else if items[0].Status != "failed" || !items[0].CanRetry {
			t.Fatal(items)
		}
	}
	*now = now.Add(time.Minute)
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("exhausted retry restarted automatically")
	}
	if err := s.RetryNotification(id); err != nil {
		t.Fatal(err)
	}
	delivered(t, s, claim(t, s))
	if err := s.RetryNotification(id); err == nil {
		t.Fatal("sent record retried")
	}
}

func TestNotificationClaimRecoveryAndTestWhileDisabled(t *testing.T) {
	s, now := fixture(t)
	cfg := enableFeishu(t, s)
	_, err := s.SaveFeishuSettings(model.FeishuSettingsInput{Categories: cfg.Categories, ExpectedRevision: cfg.Revision})
	if err != nil {
		t.Fatal(err)
	}
	test, err := s.QueueFeishuTest()
	if err != nil {
		t.Fatal(err)
	}
	if test.Category != "test" {
		t.Fatal(test)
	}
	_, err = s.QueueFeishuTest()
	if !errors.Is(err, ErrNotificationRateLimited) {
		t.Fatalf("test limit %v", err)
	}
	first := claim(t, s)
	if first == nil {
		t.Fatal("disabled test not sent")
	}
	*now = now.Add(31 * time.Second)
	if claim(t, s) != nil {
		t.Fatal("recovered claim needs backoff")
	}
	*now = now.Add(31 * time.Second)
	second := claim(t, s)
	if second == nil || second.Claim == first.Claim {
		t.Fatal("claim not recovered")
	}
	if err = s.CompleteNotificationBatch(first, true, false, ""); err != nil {
		t.Fatal(err)
	}
	items := notifications(t, s)
	if items[0].Status != "sending" {
		t.Fatal("stale completion accepted")
	}
	delivered(t, s, second)
}

func TestNotificationJobFailureTransactionalCancellationAndNoReplay(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	n := newNode(t, s, "job-notice")
	job, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.FinishJob(job, false)
	evaluate(t, s)
	batch := claim(t, s)
	if batch == nil || len(batch.Records) != 1 || !strings.Contains(batch.Records[0].Title, "失败") {
		t.Fatalf("job batch %+v", batch)
	}
	delivered(t, s, batch)
	*now = now.Add(time.Minute)
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("job replay")
	}
	if _, err = s.DB.Exec("INSERT INTO jobs(node_id,kind,state,created_at) VALUES(?,'verify','cancelling',?)", n.ID, now.Unix()); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = s.DB.QueryRow("SELECT MAX(id) FROM jobs").Scan(&id); err != nil {
		t.Fatal(err)
	}
	s.FinishJob(Job{ID: id, NodeID: n.ID, Kind: "verify"}, false)
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("cancellation alerted")
	}
	var sourceCount int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM notification_job_sources").Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("sources%d", sourceCount)
	}
}

func TestNotificationQueueBoundAndRetention(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	c, err := readNotificationConfig(tx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < NotificationQueueLimit; i++ {
		if _, err = queueNotification(tx, c, now.Unix(), model.NotificationCondition{Category: "test"}, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = queueNotification(tx, c, now.Unix(), model.NotificationCondition{Category: "test"}, nil, 0); !errors.Is(err, ErrNotificationQueueFull) {
		t.Fatalf("cap %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE notifications SET status='sent'"); err != nil {
		t.Fatal(err)
	}
	*now = now.AddDate(0, 0, 91)
	evaluate(t, s)
	if len(notifications(t, s)) != 0 {
		t.Fatal("retention failed")
	}
}

func TestNotificationFailedContinuousReminderCannotStorm(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a := condition("node.health:storm", 1, true)
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	*now = now.Add(6 * time.Hour)
	evaluate(t, s, a)
	batch := claim(t, s)
	if batch == nil {
		t.Fatal("missing6h reminder")
	}
	if err := s.CompleteNotificationBatch(batch, false, false, "飞书签名配置错误"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		*now = now.Add(30 * time.Second)
		evaluate(t, s, a)
		if claim(t, s) != nil {
			t.Fatal("permanent failure produced notification storm")
		}
	}
	if len(notifications(t, s)) != 2 {
		t.Fatal("failed reminder duplicate")
	}
	*now = now.Add(6 * time.Hour)
	evaluate(t, s, a)
	if claim(t, s) == nil {
		t.Fatal("next reminder cadence not restored")
	}
}

func TestNotificationUnknownDoesNotAccrueUnhealthyOrRecoveryTime(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	a := condition("node.health:unknown", 1, true)
	a.DelaySeconds = 60
	a.RecoverySeconds = 30
	evaluate(t, s, a)
	*now = now.Add(time.Minute)
	evaluate(t, s, a)
	unknown := a
	unknown.Unknown = true
	evaluate(t, s, unknown)
	*now = now.Add(time.Hour)
	evaluate(t, s, unknown)
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("unknown time counted toward unhealthy delay")
	}
	*now = now.Add(time.Minute)
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	evaluate(t, s)
	*now = now.Add(20 * time.Second)
	evaluate(t, s, unknown)
	*now = now.Add(time.Hour)
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("unknown time counted toward healthy stability")
	}
	*now = now.Add(30 * time.Second)
	evaluate(t, s)
	b := claim(t, s)
	if b == nil || b.Records[0].Category != "recovery" {
		t.Fatalf("expected delayed recovery %+v", b)
	}
}

func TestNotificationRemovalInvalidatesInFlightAndSuppressesRecovery(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a := condition("node.health:removed", 1, true)
	evaluate(t, s, a)
	batch := claim(t, s)
	removed := a
	removed.Removed = true
	evaluate(t, s, removed)
	delivered(t, s, batch)
	*now = now.Add(time.Minute)
	evaluate(t, s, removed)
	if claim(t, s) != nil {
		t.Fatal("deleted resource falsely recovered")
	}
	var notified bool
	if err := s.DB.QueryRow("SELECT notified FROM notification_states WHERE key=?", a.Key).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified {
		t.Fatal("stale completion resurrected deleted incident")
	}
}

func TestNotificationUnknownBacklogDoesNotStarveReady(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	conditions := []model.NotificationCondition{}
	for i := 0; i < 110; i++ {
		conditions = append(conditions, condition("unknown:"+strings.Repeat("x", i+1), 80, false))
	}
	evaluate(t, s, conditions...)
	for i := range conditions {
		conditions[i].Unknown = true
	}
	evaluate(t, s, conditions...)
	*now = now.Add(time.Minute)
	conditions = append(conditions, condition("ready", 80, false))
	evaluate(t, s, conditions...)
	batch := claim(t, s)
	if batch == nil || len(batch.Records) != 1 {
		t.Fatalf("starved by unknown backlog %+v", batch)
	}
}

func TestNotificationConfigSummaryPreservesThresholdHighWater(t *testing.T) {
	s, now := fixture(t)
	cfg := enableFeishu(t, s)
	a := condition("monthly", 100, false)
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	categories := cfg.Categories
	categories.Certificate = false
	if _, err := s.SaveFeishuSettings(model.FeishuSettingsInput{Enabled: true, Categories: categories, ExpectedRevision: cfg.Revision}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	a.Stage = 80
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	*now = now.Add(time.Minute)
	a.Stage = 90
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("config edit reset quota threshold history")
	}
}

func TestNotificationManualRetryHasDurableCooldown(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a := condition("manual", 80, false)
	evaluate(t, s, a)
	b := claim(t, s)
	if err := s.CompleteNotificationBatch(b, false, false, "配置错误"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if err := s.RetryNotification(b.Records[0].ID); err != nil {
		t.Fatal(err)
	}
	b = claim(t, s)
	if err := s.CompleteNotificationBatch(b, false, false, "配置错误"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryNotification(b.Records[0].ID); !errors.Is(err, ErrNotificationRateLimited) {
		t.Fatalf("retry cooldown %v", err)
	}
}

func TestNotificationUnknownPausesAlreadyQueuedRecovery(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a := condition("node.health:recovery", 1, true)
	a.RecoverySeconds = 30
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	evaluate(t, s)
	*now = now.Add(30 * time.Second)
	evaluate(t, s)
	unknown := a
	unknown.Unknown = true
	evaluate(t, s, unknown)
	*now = now.Add(time.Hour)
	if claim(t, s) != nil {
		t.Fatal("unknown state delivered queued recovery")
	}
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("recovery reused unknown stability period")
	}
	*now = now.Add(30 * time.Second)
	evaluate(t, s)
	if claim(t, s) == nil {
		t.Fatal("stable recovery did not resume")
	}
}

func TestNotificationLongEntitlementKeepsThresholdDedupPastLogRetention(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a := condition("package.quota:user:epoch", 80, false)
	a.Category = "package"
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	evaluate(t, s)
	*now = now.AddDate(0, 0, 91)
	evaluate(t, s)
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("log retention erased entitlement threshold dedup")
	}
}

func TestNotificationJobFailureCancelledWhenNodeDeleted(t *testing.T) {
	s, _ := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	n := newNode(t, s, "deleted-job")
	job, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.FinishJob(job, false)
	evaluate(t, s)
	if _, err = s.DB.Exec("UPDATE nodes SET lifecycle='deleted' WHERE id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	if claim(t, s) != nil {
		t.Fatal("deleted node job failure delivered")
	}
}

func TestNotificationStartupQuickRestartDoesNotReplay(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a := condition("monthly:startup", 80, false)
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	*now = now.Add(119 * time.Second)
	if err := s.PrepareNotificationRun(); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("quick restart replayed threshold")
	}
	*now = now.Add(120 * time.Second)
	if err := s.PrepareNotificationRun(); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, a)
	b := claim(t, s)
	if b == nil || b.Records[0].Category != "recovery-summary" {
		t.Fatalf("long outage summary %+v", b)
	}
	delivered(t, s, b)
	*now = now.Add(30 * time.Second)
	evaluate(t, s, a)
	if claim(t, s) != nil {
		t.Fatal("restart summary repeated")
	}
	var stage int
	if err := s.DB.QueryRow("SELECT max_stage FROM notification_states WHERE key=?", a.Key).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if stage != 80 {
		t.Fatal("restart reset threshold highwater")
	}
}

func TestNotificationStartupCoreMustBeObservedForFreshMinute(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	evaluate(t, s)
	core := condition("node.health:startup", 1, true)
	core.DelaySeconds = 60
	core.RecoverySeconds = 30
	evaluate(t, s, core)
	*now = now.Add(10 * time.Minute)
	if err := s.PrepareNotificationRun(); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, core)
	if claim(t, s) != nil {
		t.Fatal("outage accrued core unhealthy time")
	}
	*now = now.Add(59 * time.Second)
	evaluate(t, s, core)
	if claim(t, s) != nil {
		t.Fatal("core notified before fresh60")
	}
	*now = now.Add(time.Second)
	evaluate(t, s, core)
	b := claim(t, s)
	if b == nil || b.Records[0].Category != "recovery-summary" {
		t.Fatalf("fresh core resume summary %+v", b)
	}
	delivered(t, s, b)
	*now = now.Add(time.Minute)
	evaluate(t, s, core)
	if claim(t, s) != nil {
		t.Fatal("core duplicated after resume summary")
	}
}

func TestNotificationStartupOutageCannotCompleteRecoveryGrace(t *testing.T) {
	s, now := fixture(t)
	enableFeishu(t, s)
	a := condition("node.health:outage-recovery", 1, true)
	a.RecoverySeconds = 30
	evaluate(t, s, a)
	delivered(t, s, claim(t, s))
	evaluate(t, s)
	*now = now.Add(10 * time.Minute)
	if err := s.PrepareNotificationRun(); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("outage accrued healthy time")
	}
	*now = now.Add(29 * time.Second)
	evaluate(t, s)
	if claim(t, s) != nil {
		t.Fatal("recovery too early")
	}
	*now = now.Add(time.Second)
	evaluate(t, s)
	b := claim(t, s)
	if b == nil || b.Records[0].Category != "recovery" {
		t.Fatalf("confirmed recovery missing %+v", b)
	}
}

func TestNotificationStartupRecoveryDisabledStillSendsNewAlerts(t *testing.T) {
	s, now := fixture(t)
	cfg := enableFeishu(t, s)
	categories := cfg.Categories
	categories.Recovery = false
	if _, err := s.SaveFeishuSettings(model.FeishuSettingsInput{Enabled: true, Categories: categories, ExpectedRevision: cfg.Revision}); err != nil {
		t.Fatal(err)
	}
	old := condition("old-capacity", 80, false)
	evaluate(t, s, old)
	delivered(t, s, claim(t, s))
	*now = now.Add(120 * time.Second)
	if err := s.PrepareNotificationRun(); err != nil {
		t.Fatal(err)
	}
	newCondition := condition("new-capacity", 80, false)
	evaluate(t, s, old, newCondition)
	b := claim(t, s)
	if b == nil || len(b.Records) != 1 || b.Records[0].Category != "capacity" {
		t.Fatalf("recovery disabled suppressed new alert %+v", b)
	}
	delivered(t, s, b)
	*now = now.Add(time.Minute)
	evaluate(t, s, old, newCondition)
	if claim(t, s) != nil {
		t.Fatal("disabled recovery summary delivered")
	}
}

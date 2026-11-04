package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/feishu"
	"github.com/KeepS13/HQIP/internal/model"
)

const NotificationQueueLimit = 1000

var ErrNotificationRateLimited = errors.New("操作过于频繁，请稍后重试")
var ErrNotificationQueueFull = errors.New("通知队列已满，请检查投递配置")

func EnsureNotificationSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS feishu_settings(id INTEGER PRIMARY KEY CHECK(id=1),enabled INTEGER NOT NULL DEFAULT 0,webhook TEXT NOT NULL DEFAULT '',secret TEXT NOT NULL DEFAULT '',categories TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 0,initialized INTEGER NOT NULL DEFAULT 0,last_success INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',last_dispatch INTEGER NOT NULL DEFAULT 0,last_test INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO feishu_settings(id,categories) VALUES(1,'{"system":true,"capacity":true,"certificate":true,"package":true,"recovery":true}');
CREATE TABLE IF NOT EXISTS notification_states(key TEXT PRIMARY KEY,condition TEXT NOT NULL,active INTEGER NOT NULL,observable INTEGER NOT NULL DEFAULT 1,since INTEGER NOT NULL,missing_since INTEGER NOT NULL DEFAULT 0,max_stage INTEGER NOT NULL DEFAULT 0,notified INTEGER NOT NULL DEFAULT 0,last_sent INTEGER NOT NULL DEFAULT 0,last_queued INTEGER NOT NULL DEFAULT 0,recovery_queued INTEGER NOT NULL DEFAULT 0,episode INTEGER NOT NULL DEFAULT 1,last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS notifications(id TEXT PRIMARY KEY,category TEXT NOT NULL,level TEXT NOT NULL,title TEXT NOT NULL,message TEXT NOT NULL,detail_path TEXT NOT NULL,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,cycle_attempts INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,next_attempt INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL,refs TEXT NOT NULL DEFAULT '[]',job_id INTEGER NOT NULL DEFAULT 0,claim TEXT NOT NULL DEFAULT '',claim_until INTEGER NOT NULL DEFAULT 0,last_retry INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS notification_due ON notifications(status,next_attempt,created_at);
CREATE INDEX IF NOT EXISTS notification_created ON notifications(created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS notification_job ON notifications(job_id) WHERE job_id>0;
CREATE TABLE IF NOT EXISTS notification_job_sources(job_id INTEGER PRIMARY KEY,revision INTEGER NOT NULL,created_at INTEGER NOT NULL,processed INTEGER NOT NULL DEFAULT 0);
`)
	return err
}

type notificationConfig struct {
	Enabled                bool
	Webhook, Secret        string
	Categories             model.NotificationCategories
	Revision               int64
	Initialized            bool
	LastSuccess            int64
	LastError              string
	LastDispatch, LastTest int64
}

type notificationQuerier interface{ QueryRow(string, ...any) *sql.Row }

func readNotificationConfig(q notificationQuerier) (notificationConfig, error) {
	var c notificationConfig
	var raw string
	err := q.QueryRow("SELECT enabled,webhook,secret,categories,revision,initialized,last_success,last_error,last_dispatch,last_test FROM feishu_settings WHERE id=1").Scan(&c.Enabled, &c.Webhook, &c.Secret, &raw, &c.Revision, &c.Initialized, &c.LastSuccess, &c.LastError, &c.LastDispatch, &c.LastTest)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c.Categories)
	}
	return c, err
}

func (s *Store) FeishuSettings() (model.FeishuSettings, error) {
	c, err := readNotificationConfig(s.DB)
	if err != nil {
		return model.FeishuSettings{}, err
	}
	out := model.FeishuSettings{Enabled: c.Enabled, WebhookConfigured: c.Webhook != "", SecretConfigured: c.Secret != "", Categories: c.Categories, Revision: c.Revision, LastError: c.LastError}
	if c.Webhook != "" {
		out.WebhookMasked = "https://open.feishu.cn/open-apis/bot/v2/hook/••••••••"
	}
	if c.LastSuccess > 0 {
		t := time.Unix(c.LastSuccess, 0).UTC()
		out.LastSuccessAt = &t
	}
	err = s.DB.QueryRow("SELECT COUNT(*) FROM notifications WHERE status IN ('pending','retry','sending')").Scan(&out.QueueDepth)
	return out, err
}

func (s *Store) SaveFeishuSettings(in model.FeishuSettingsInput) (model.FeishuSettings, error) {
	var out model.FeishuSettings
	if in.ClearCredentials && (in.Enabled || in.Webhook != "" || in.Secret != "") {
		return out, errors.New("清除凭据时须关闭通知，且不能同时填写新凭据")
	}
	if in.Webhook != "" {
		if err := feishu.ValidateWebhook(in.Webhook); err != nil {
			return out, err
		}
	}
	if in.Secret != "" && (len(in.Secret) > 512 || strings.TrimSpace(in.Secret) != in.Secret || strings.ContainsAny(in.Secret, "\r\n\x00")) {
		return out, errors.New("签名密钥格式无效")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return out, err
	}
	if c.Revision != in.ExpectedRevision {
		return out, ErrConflict
	}
	webhook, secret := c.Webhook, c.Secret
	if in.ClearCredentials {
		webhook = ""
		secret = ""
	}
	if in.Webhook != "" {
		webhook, err = s.Vault.Encrypt(in.Webhook)
		if err != nil {
			return out, err
		}
	}
	if in.Secret != "" {
		secret, err = s.Vault.Encrypt(in.Secret)
		if err != nil {
			return out, err
		}
	}
	if in.Enabled && (webhook == "" || secret == "") {
		return out, errors.New("启用通知需要 Webhook 地址和签名密钥")
	}
	// Every configuration revision gets a fresh current-state summary. Unsent
	// records of the previous revision can never use replacement credentials.
	if _, err = tx.Exec("UPDATE notifications SET status='cancelled',last_error='通知配置已变更',claim='',claim_until=0,next_attempt=0 WHERE status IN ('pending','retry','sending')"); err != nil {
		return out, err
	}
	if _, err = tx.Exec("UPDATE feishu_settings SET enabled=?,webhook=?,secret=?,categories=?,revision=revision+1,initialized=0,last_error='' WHERE id=1", in.Enabled, webhook, secret, jsonString(in.Categories)); err != nil {
		return out, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'feishu.settings','notification configuration')", s.Now().Unix()); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return s.FeishuSettings()
}

type notificationRef struct {
	Key      string `json:"key"`
	Stage    int    `json:"stage"`
	Episode  int    `json:"episode"`
	Recovery bool   `json:"recovery,omitempty"`
}
type notificationState struct {
	Condition                                           model.NotificationCondition
	Active, Observable                                  bool
	Since, MissingSince, LastSent, LastSeen, LastQueued int64
	MaxStage, Episode                                   int
	Notified, RecoveryQueued                            bool
}

func loadNotificationStates(tx *sql.Tx) (map[string]*notificationState, error) {
	rows, err := tx.Query("SELECT key,condition,active,observable,since,missing_since,max_stage,notified,last_sent,last_queued,recovery_queued,episode,last_seen FROM notification_states")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*notificationState{}
	for rows.Next() {
		var key, raw string
		v := &notificationState{}
		if err = rows.Scan(&key, &raw, &v.Active, &v.Observable, &v.Since, &v.MissingSince, &v.MaxStage, &v.Notified, &v.LastSent, &v.LastQueued, &v.RecoveryQueued, &v.Episode, &v.LastSeen); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v.Condition); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, rows.Err()
}

func saveNotificationState(tx *sql.Tx, v *notificationState) error {
	_, err := tx.Exec(`INSERT INTO notification_states(key,condition,active,observable,since,missing_since,max_stage,notified,last_sent,last_queued,recovery_queued,episode,last_seen) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET condition=excluded.condition,active=excluded.active,observable=excluded.observable,since=excluded.since,missing_since=excluded.missing_since,max_stage=excluded.max_stage,notified=excluded.notified,last_sent=excluded.last_sent,last_queued=excluded.last_queued,recovery_queued=excluded.recovery_queued,episode=excluded.episode,last_seen=excluded.last_seen`, v.Condition.Key, jsonString(v.Condition), v.Active, v.Observable, v.Since, v.MissingSince, v.MaxStage, v.Notified, v.LastSent, v.LastQueued, v.RecoveryQueued, v.Episode, v.LastSeen)
	return err
}

func queueNotification(tx *sql.Tx, c notificationConfig, now int64, condition model.NotificationCondition, refs []notificationRef, jobID int64) (string, error) {
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM notifications WHERE status IN ('pending','retry','sending')").Scan(&count); err != nil {
		return "", err
	}
	if count >= NotificationQueueLimit {
		return "", ErrNotificationQueueFull
	}
	id := NewID()
	_, err := tx.Exec("INSERT INTO notifications(id,category,level,title,message,detail_path,status,created_at,next_attempt,revision,refs,job_id) VALUES(?,?,?,?,?,?,'pending',?,?,?,?,?)", id, condition.Category, condition.Level, condition.Title, condition.Message, condition.DetailPath, now, now, c.Revision, jsonString(refs), jobID)
	return id, err
}

// EvaluateNotifications accepts a complete successful scan. Callers must not
// invoke it with an empty list after a scan error: absent means resolved.
func (s *Store) EvaluateNotifications(conditions []model.NotificationCondition) error {
	now := s.Now().Unix()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('notification_last_evaluation',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", strconv.FormatInt(now, 10)); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM notifications WHERE created_at<? AND status NOT IN ('pending','retry','sending')", s.Now().AddDate(0, 0, -90).Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM notification_job_sources WHERE created_at<? OR revision!=?", s.Now().AddDate(0, 0, -90).Unix(), c.Revision); err != nil {
		return err
	}
	if !c.Enabled {
		return tx.Commit()
	}
	states, err := loadNotificationStates(tx)
	if err != nil {
		return err
	}
	var recoveryRaw string
	err = tx.QueryRow("SELECT value FROM metadata WHERE key='notification_startup_recovery'").Scan(&recoveryRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	recoveryStart, _ := strconv.ParseInt(recoveryRaw, 10, 64)
	if recoveryStart == -1 {
		recoveryStart = now
		if _, err = tx.Exec("UPDATE metadata SET value=? WHERE key='notification_startup_recovery'", strconv.FormatInt(now, 10)); err != nil {
			return err
		}
		for _, v := range states {
			v.Since = now
			v.MissingSince = 0
			v.Observable = false
			if v.Active {
				v.RecoveryQueued = false
				if v.Condition.Continuous && !v.Notified {
					v.MaxStage = 0
				}
			}
		}
		if _, err = tx.Exec("UPDATE notifications SET status='cancelled',next_attempt=0,claim='',claim_until=0,last_error='控制服务恢复后重新核对当前状态' WHERE status IN ('pending','retry','sending') AND job_id=0 AND category NOT IN ('test','recovery')"); err != nil {
			return err
		}
	}
	resuming := c.Initialized && recoveryStart > 0 && c.Categories.Recovery
	if recoveryStart > 0 && !c.Categories.Recovery {
		if _, err = tx.Exec("UPDATE metadata SET value='0' WHERE key='notification_startup_recovery'"); err != nil {
			return err
		}
	}
	observed := map[string]bool{}
	for _, condition := range conditions {
		if condition.Key == "" || !c.Categories.Allows(condition.Category) {
			continue
		}
		observed[condition.Key] = true
		v := states[condition.Key]
		if condition.Removed {
			if v != nil {
				if !v.Condition.Removed {
					v.Episode++
				}
				v.Condition.Removed = true
				v.Active = false
				v.Notified = false
				v.RecoveryQueued = true
				v.LastSeen = now
				v.MissingSince = now
			}
			continue
		}
		if condition.Unknown {
			if v != nil {
				v.Observable = false
				v.LastSeen = now
				v.Since = now
				v.MissingSince = 0
			}
			continue
		}
		if condition.Stage < 1 {
			condition.Stage = 1
		}
		if v == nil {
			v = &notificationState{Condition: condition, Active: true, Since: now, Episode: 1}
			states[condition.Key] = v
		} else if !v.Active {
			v.Active = true
			v.Since = now
			v.MissingSince = 0
			v.RecoveryQueued = false
			if condition.Continuous {
				v.Episode++
				v.MaxStage = 0
				v.Notified = false
				v.LastSent = 0
				v.LastQueued = 0
			}
		} else if !v.Observable || condition.DelaySeconds > v.Condition.DelaySeconds || v.MissingSince > 0 {
			v.Since = now
		}
		v.Condition = condition
		v.Observable = true
		v.LastSeen = now
		v.MissingSince = 0
	}
	var initial []notificationRef
	resumeReady := true
	if resuming {
		for key, v := range states {
			if observed[key] && !v.Condition.Removed && v.Active && (!v.Observable || now-v.Since < int64(v.Condition.DelaySeconds)) {
				resumeReady = false
			}
		}
	}
	if resuming && now-recoveryStart >= 60 {
		resumeReady = true
	}
	for key, v := range states {
		if !observed[key] {
			if v.MissingSince == 0 {
				v.MissingSince = now
			}
			v.Observable = true
			if v.Active && now-v.MissingSince >= int64(v.Condition.RecoverySeconds) {
				v.Active = false
			}
		}
		if resuming {
			if resumeReady && v.Active && v.Observable && observed[key] && !v.Condition.Removed && now-v.Since >= int64(v.Condition.DelaySeconds) && c.Categories.Allows(v.Condition.Category) {
				initial = append(initial, notificationRef{Key: key, Stage: v.Condition.Stage, Episode: v.Episode})
				if v.Condition.Stage > v.MaxStage {
					v.MaxStage = v.Condition.Stage
				}
				v.LastQueued = now
			}
		} else if !c.Initialized {
			// New destinations get only current eligible conditions, never a
			// backlog of the old destination's thresholds or job failures.
			v.Notified = false
			v.RecoveryQueued = false
			v.LastSent = 0
			if v.Active && v.Observable && observed[key] && now-v.Since >= int64(v.Condition.DelaySeconds) && c.Categories.Allows(v.Condition.Category) {
				initial = append(initial, notificationRef{Key: key, Stage: v.Condition.Stage, Episode: v.Episode})
				if v.Condition.Stage > v.MaxStage {
					v.MaxStage = v.Condition.Stage
				}
				v.LastQueued = now
			}
		} else if v.Active && v.Observable && observed[key] && now-v.Since >= int64(v.Condition.DelaySeconds) && c.Categories.Allows(v.Condition.Category) {
			repeat := v.Condition.Continuous && v.Notified && now-v.LastSent >= 6*3600 && now-v.LastQueued >= 6*3600
			if v.Condition.Stage > v.MaxStage || repeat {
				var pending int
				if err = tx.QueryRow("SELECT COUNT(*) FROM notifications WHERE status IN ('pending','retry','sending') AND EXISTS(SELECT 1 FROM json_each(refs) WHERE json_extract(value,'$.key')=? AND COALESCE(json_extract(value,'$.recovery'),0)=0)", key).Scan(&pending); err != nil {
					return err
				}
				if pending == 0 || v.Condition.Stage > v.MaxStage {
					_, e := queueNotification(tx, c, now, v.Condition, []notificationRef{{Key: key, Stage: v.Condition.Stage, Episode: v.Episode}}, 0)
					if e == nil {
						v.LastQueued = now
						if v.Condition.Stage > v.MaxStage {
							v.MaxStage = v.Condition.Stage
						}
					} else if !errors.Is(e, ErrNotificationQueueFull) {
						return e
					}
				}
			}
		} else if !v.Active && v.Condition.Continuous && v.Notified && !v.RecoveryQueued && c.Categories.Recovery && c.Categories.Allows(v.Condition.Category) {
			condition := v.Condition
			condition.Category = "recovery"
			condition.Level = "info"
			condition.Title = "已恢复：" + condition.Title
			condition.Message = "此前通知的异常已恢复，最近状态已确认。"
			_, e := queueNotification(tx, c, now, condition, []notificationRef{{Key: key, Stage: v.Condition.Stage, Episode: v.Episode, Recovery: true}}, 0)
			if e == nil {
				v.RecoveryQueued = true
			} else if !errors.Is(e, ErrNotificationQueueFull) {
				return e
			}
		}
		if err = saveNotificationState(tx, v); err != nil {
			return err
		}
	}
	if resuming && resumeReady {
		if len(initial) > 0 {
			condition := summaryCondition(initial, states)
			condition.Category = "recovery-summary"
			condition.Title = "HQIP 控制服务恢复状态摘要"
			condition.Message = "控制服务监控中断至少 120 秒后已恢复，以下为重新核对的当前状态。\n" + condition.Message
			if _, err = queueNotification(tx, c, now, condition, initial, 0); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE metadata SET value='0' WHERE key='notification_startup_recovery'"); err != nil {
			return err
		}
	}
	if !c.Initialized {
		if len(initial) > 0 {
			condition := summaryCondition(initial, states)
			if _, err = queueNotification(tx, c, now, condition, initial, 0); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE feishu_settings SET initialized=1 WHERE id=1"); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE metadata SET value='0' WHERE key='notification_startup_recovery'"); err != nil {
			return err
		}
	}
	// Delivery history expires after90d, but threshold high-water marks must
	// survive long-lived entitlement epochs and quota increases/decreases.
	if _, err = tx.Exec("DELETE FROM notification_states WHERE active=0 AND json_extract(condition,'$.continuous')=1 AND last_seen<? AND NOT EXISTS(SELECT 1 FROM notifications,json_each(notifications.refs) WHERE notifications.status IN ('pending','retry','sending') AND json_extract(json_each.value,'$.key')=notification_states.key)", s.Now().AddDate(0, 0, -90).Unix()); err != nil {
		return err
	}
	// Invalidate before the next claim. Sending claims are handled by their
	// revision/claim token, because a network request cannot be unsent.
	if err = refreshPendingNotifications(tx, c, states, now); err != nil {
		return err
	}
	if err = queueJobNotifications(tx, c, now); err != nil {
		return err
	}
	return tx.Commit()
}

// PrepareNotificationRun is called once by the background worker, not by every
// scan. A quick restart retains incident dedup; an extended controller outage
// receives one fresh-state summary without treating offline time as evidence.
func (s *Store) PrepareNotificationRun() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return err
	}
	var raw string
	err = tx.QueryRow("SELECT value FROM metadata WHERE key='notification_last_evaluation'").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	last, _ := strconv.ParseInt(raw, 10, 64)
	if c.Enabled && c.Initialized && last > 0 && s.Now().Unix()-last >= 120 {
		_, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('notification_startup_recovery','-1') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func summaryCondition(refs []notificationRef, states map[string]*notificationState) model.NotificationCondition {
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key < refs[j].Key })
	lines := []string{fmt.Sprintf("当前共有 %d 项需要关注：", len(refs))}
	for i, ref := range refs {
		if i >= 20 {
			lines = append(lines, "其余项目请在后台查看。")
			break
		}
		v := states[ref.Key]
		lines = append(lines, v.Condition.Title+"："+v.Condition.Message)
	}
	return model.NotificationCondition{Category: "summary", Level: "warning", Title: "HQIP 当前异常摘要", Message: strings.Join(lines, "\n"), DetailPath: "/admin/notifications"}
}

// Source creation shares the job terminal transaction. Delivery backpressure
// cannot lose the event or roll back successful business state changes.
func recordJobNotification(tx *sql.Tx, jobID, now int64) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO notification_job_sources(job_id,revision,created_at) SELECT ?,revision,? FROM feishu_settings WHERE id=1 AND enabled=1 AND json_extract(categories,'$.system')=1`, jobID, now)
	return err
}

func queueJobNotifications(tx *sql.Tx, c notificationConfig, now int64) error {
	rows, err := tx.Query(`SELECT src.job_id,j.node_id,j.kind,COALESCE(json_extract(n.config,'$.name'),'节点'),j.state FROM notification_job_sources src JOIN jobs j ON j.id=src.job_id JOIN nodes n ON n.id=j.node_id WHERE src.processed=0 AND src.revision=? ORDER BY src.created_at LIMIT 100`, c.Revision)
	if err != nil {
		return err
	}
	type item struct {
		id                      int64
		node, kind, name, state string
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.node, &v.kind, &v.name, &v.state); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if v.state == "error" && c.Categories.System {
			kind := map[string]string{"install": "安装", "verify": "验证", "retire": "清理"}[v.kind]
			if kind == "" {
				kind = "部署"
			}
			condition := model.NotificationCondition{Category: "system", Level: "danger", Title: "节点" + kind + "失败", Message: "节点「" + v.name + "」的" + kind + "任务最终失败，请在后台查看脱敏任务详情。", DetailPath: "/admin/nodes/" + v.node}
			if _, err = queueNotification(tx, c, now, condition, nil, v.id); errors.Is(err, ErrNotificationQueueFull) {
				return nil
			} else if err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE notification_job_sources SET processed=1 WHERE job_id=?", v.id); err != nil {
			return err
		}
	}
	return nil
}

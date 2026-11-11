package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KeepS13/HQIP/internal/model"
)

// EnsureNotificationSchema retires delivery once, retaining business history.
// No sender, credentials or enabled switch participate in the alert evaluator.
func EnsureNotificationSchema(db *sql.DB) error {
	_, err := db.Exec(`
 CREATE TABLE IF NOT EXISTS alerts(id TEXT PRIMARY KEY,key TEXT NOT NULL UNIQUE,condition TEXT NOT NULL,status TEXT NOT NULL,acknowledged INTEGER NOT NULL DEFAULT 0,ack_by TEXT NOT NULL DEFAULT '',ack_at INTEGER NOT NULL DEFAULT 0,first_seen INTEGER NOT NULL,last_seen INTEGER NOT NULL,ended_at INTEGER NOT NULL DEFAULT 0,revision INTEGER NOT NULL DEFAULT 1,since INTEGER NOT NULL,missing_since INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS alerts_current ON alerts(status,acknowledged,last_seen);
 CREATE TABLE IF NOT EXISTS alert_activity(id INTEGER PRIMARY KEY AUTOINCREMENT,alert_id TEXT NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,kind TEXT NOT NULL,message TEXT NOT NULL,actor TEXT NOT NULL DEFAULT '',at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS alert_activity_object ON alert_activity(alert_id,id);
 CREATE TABLE IF NOT EXISTS alert_job_sources(job_id INTEGER PRIMARY KEY,created_at INTEGER NOT NULL);
 `)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var migrated int
	if err = tx.QueryRow("SELECT COUNT(*) FROM metadata WHERE key='alerts_migrated'").Scan(&migrated); err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='feishu_settings'").Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		if _, err = tx.Exec("UPDATE feishu_settings SET enabled=0,webhook='',secret=''"); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE notifications SET status='cancelled',claim='',claim_until=0,next_attempt=0,last_error='已迁移至站内预警中心' WHERE status IN ('pending','sending','retry')"); err != nil {
			return err
		}
	}
	if migrated == 0 && exists > 0 {
		rows, e := tx.Query("SELECT id,category,level,title,message,detail_path,created_at FROM notifications WHERE category IN ('system','capacity','certificate','recovery') AND detail_path NOT LIKE '%/users/%' ORDER BY created_at")
		if e != nil {
			return e
		}
		type legacy struct {
			id string
			c  model.NotificationCondition
			at int64
		}
		items := []legacy{}
		for rows.Next() {
			var x legacy
			if err = rows.Scan(&x.id, &x.c.Category, &x.c.Level, &x.c.Title, &x.c.Message, &x.c.DetailPath, &x.at); err != nil {
				rows.Close()
				return err
			}
			items = append(items, x)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, x := range items {
			x.c.DetailPath = alertPath(x.c.DetailPath)
			x.c.Kind = "legacy"
			x.c.Key = "legacy:" + x.id
			if _, err = tx.Exec("INSERT OR IGNORE INTO alerts(id,key,condition,status,first_seen,last_seen,ended_at,since) VALUES(?,?,?,'ended',?,?,?,?)", x.id, x.c.Key, jsonString(x.c), x.at, x.at, x.at, x.at); err != nil {
				return err
			}
		}
	}
	if migrated == 0 {
		// Seed currently unresolved terminal jobs, including failures recorded
		// while the former robot was disabled. Do not replay succeeded work.
		if _, err = tx.Exec(`INSERT OR IGNORE INTO alert_job_sources(job_id,created_at)
		 SELECT j.id,MAX(j.created_at,j.started_at) FROM jobs j JOIN nodes n ON n.id=j.node_id
		 WHERE j.state='error' AND n.lifecycle!='deleted'
		 AND NOT EXISTS(SELECT 1 FROM jobs later WHERE later.node_id=j.node_id AND later.kind=j.kind AND later.id>j.id AND later.state IN ('done','error'))`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO metadata(key,value) VALUES('alerts_migrated','1')"); err != nil {
		return err
	}
	// User subscription state belongs to that user's pages, never to the
	// infrastructure alert center. Remove both incidents and attached notes.
	const userAlert = `json_extract(condition,'$.category')='package' OR json_extract(condition,'$.kind') LIKE 'package.%' OR json_extract(condition,'$.detailPath') LIKE '%/users/%'`
	if _, err = tx.Exec("DELETE FROM alert_activity WHERE alert_id IN (SELECT id FROM alerts WHERE " + userAlert + ")"); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM alerts WHERE " + userAlert); err != nil {
		return err
	}
	return tx.Commit()
}

func alertPath(path string) string {
	path = strings.TrimPrefix(path, "/admin")
	if path == "/domains" || path == "/notifications" {
		return path
	}
	for _, prefix := range []string{"/nodes/", "/servers/", "/users/"} {
		if strings.HasPrefix(path, prefix) {
			id := strings.TrimPrefix(path, prefix)
			if id != "" && strings.IndexFunc(id, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
			}) < 0 {
				return path
			}
		}
	}
	return "/notifications"
}

// Called in the deployment terminal-state transaction, regardless of UI use.
func recordJobNotification(tx *sql.Tx, jobID, now int64) error {
	_, err := tx.Exec("INSERT OR IGNORE INTO alert_job_sources(job_id,created_at) VALUES(?,?)", jobID, now)
	return err
}

type alertRow struct {
	model.Alert
	since, missing int64
}

const alertColumns = "id,key,condition,status,acknowledged,ack_by,ack_at,first_seen,last_seen,ended_at,revision,since,missing_since"

// Derive the policy at read time without rewriting human audit history.
// Keep in sync with model.NotificationCondition.RequiresAcknowledgement.
const alertAcknowledgementRequiredSQL = "COALESCE(json_extract(condition,'$.kind'),'') NOT IN ('" + model.DeploymentFailureKind + "','node.month','node.cycle')"

type alertScanner interface{ Scan(...any) error }

func readAlert(row alertScanner) (alertRow, error) {
	var a alertRow
	var raw string
	var ack, first, last, end int64
	err := row.Scan(&a.ID, &a.Key, &raw, &a.Status, &a.Acknowledged, &a.AcknowledgedBy, &ack, &first, &last, &end, &a.Revision, &a.since, &a.missing)
	if err != nil {
		return a, err
	}
	err = json.Unmarshal([]byte(raw), &a.Condition)
	a.AcknowledgementRequired = a.Condition.RequiresAcknowledgement()
	a.FirstSeen = time.Unix(first, 0).UTC()
	a.LastSeen = time.Unix(last, 0).UTC()
	a.AcknowledgedAt = alertTime(ack)
	a.EndedAt = alertTime(end)
	return a, err
}
func alertTime(v int64) *time.Time {
	if v == 0 {
		return nil
	}
	t := time.Unix(v, 0).UTC()
	return &t
}
func alertActivity(tx *sql.Tx, id, kind, message, actor string, now int64) error {
	_, err := tx.Exec("INSERT INTO alert_activity(alert_id,kind,message,actor,at) VALUES(?,?,?,?,?)", id, kind, message, actor, now)
	return err
}
func saveAlert(tx *sql.Tx, a alertRow) error {
	var ack, end int64
	if a.AcknowledgedAt != nil {
		ack = a.AcknowledgedAt.Unix()
	}
	if a.EndedAt != nil {
		end = a.EndedAt.Unix()
	}
	_, err := tx.Exec(`INSERT INTO alerts(`+alertColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET condition=excluded.condition,status=excluded.status,acknowledged=excluded.acknowledged,ack_by=excluded.ack_by,ack_at=excluded.ack_at,last_seen=excluded.last_seen,ended_at=excluded.ended_at,revision=excluded.revision,since=excluded.since,missing_since=excluded.missing_since`, a.ID, a.Key, jsonString(a.Condition), a.Status, a.Acknowledged, a.AcknowledgedBy, ack, a.FirstSeen.Unix(), a.LastSeen.Unix(), end, a.Revision, a.since, a.missing)
	return err
}

func appendJobConditions(tx *sql.Tx, conditions []model.NotificationCondition) ([]model.NotificationCondition, error) {
	rows, err := tx.Query(`SELECT j.id,j.node_id,j.kind,src.created_at,COALESCE(json_extract(n.config,'$.name'),'节点'),n.lifecycle,
 EXISTS(SELECT 1 FROM jobs next WHERE next.node_id=j.node_id AND next.kind=j.kind AND next.id>j.id AND next.state='done')
 FROM alert_job_sources src JOIN jobs j ON j.id=src.job_id JOIN nodes n ON n.id=j.node_id WHERE j.state='error'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at int64
		var node, kind, name, lifecycle string
		var done bool
		if err = rows.Scan(&id, &node, &kind, &at, &name, &lifecycle, &done); err != nil {
			return nil, err
		}
		c := model.NotificationCondition{Key: "job:" + strconv.FormatInt(id, 10), Category: "system", Kind: "job." + kind, ResourceID: node, Title: notificationName(name, "节点") + " 任务失败", DetailPath: "/nodes/" + node, Level: "danger", Stage: 1, Continuous: true}
		label := map[string]string{"install": "安装", "verify": "验证", "retire": "清理"}[kind]
		if label == "" {
			label = "部署"
		}
		c.Message = "节点的" + label + "任务最终失败，请查看节点任务详情。"
		if lifecycle == "deleted" || done {
			c.Removed = true
		}
		conditions = append(conditions, c)
	}
	return conditions, rows.Err()
}

// EvaluateAlerts preserves acknowledgement separately from observed health.
// A failed collection never calls this function, and unknown is not recovery.
func (s *Store) EvaluateAlerts(conditions []model.NotificationCondition) error {
	now := s.Now().Unix()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	conditions, err = appendJobConditions(tx, conditions)
	if err != nil {
		return err
	}
	var lastRaw string
	err = tx.QueryRow("SELECT value FROM metadata WHERE key='alerts_last_evaluation'").Scan(&lastRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	last, _ := strconv.ParseInt(lastRaw, 10, 64)
	gap := last > 0 && (now-last > 120 || now < last)
	rows, err := tx.Query("SELECT " + alertColumns + " FROM alerts WHERE key NOT LIKE 'legacy:%'")
	if err != nil {
		return err
	}
	states := map[string]alertRow{}
	for rows.Next() {
		a, e := readAlert(rows)
		if e != nil {
			rows.Close()
			return e
		}
		states[a.Key] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	observed := map[string]bool{}
	for _, c := range conditions {
		if c.Key == "" {
			continue
		}
		observed[c.Key] = true
		a, found := states[c.Key]
		if c.Removed {
			if found && (a.Status == "active" || a.Status == "unknown" || a.Status == "pending") {
				if err = endAlert(tx, &a, "ended", now); err != nil {
					return err
				}
				states[c.Key] = a
			}
			continue
		}
		if c.Unknown {
			if found && (a.Status == "active" || a.Status == "unknown" || a.Status == "pending") {
				a.since = now
				a.missing = 0
				a.LastSeen = s.Now().UTC()
				a.Condition.Unknown = true
				if a.Status == "active" {
					a.Status = "unknown"
					a.Revision++
					if err = alertActivity(tx, a.ID, "unknown", "数据不足，等待有效报告确认状态。", "", now); err != nil {
						return err
					}
				}
				if err = saveAlert(tx, a); err != nil {
					return err
				}
				states[c.Key] = a
			}
			continue
		}
		if c.Stage < 1 {
			c.Stage = 1
		}
		c.DetailPath = alertPath(c.DetailPath)
		previousStage := a.Condition.Stage
		previousStatus := a.Status
		if !found {
			a = alertRow{Alert: model.Alert{ID: NewID(), Key: c.Key, Condition: c, Status: "pending", FirstSeen: s.Now().UTC(), LastSeen: s.Now().UTC(), Revision: 1}, since: now}
		} else {
			if a.Status == "resolved" || a.Status == "ended" {
				a.Status = "pending"
				a.since = now
				a.Acknowledged = false
				a.AcknowledgedAt = nil
				a.AcknowledgedBy = ""
				a.EndedAt = nil
				a.Revision++
			}
			if a.missing != 0 || gap || a.Condition.Unknown {
				a.since = now
			}
		}
		a.missing = 0
		a.LastSeen = s.Now().UTC()
		a.Condition = c
		ready := now-a.since >= int64(c.DelaySeconds)
		event, message := "", ""
		if (a.Status == "pending" || a.Status == "unknown") && ready {
			a.Status = "active"
			a.Revision++
			event = "opened"
			message = c.Message
		}
		if c.Stage > previousStage && previousStatus != "" && previousStatus != "pending" {
			a.Acknowledged = false
			a.AcknowledgedAt = nil
			a.AcknowledgedBy = ""
			a.Revision++
			event = "escalated"
			message = c.Message
		}
		if err = saveAlert(tx, a); err != nil {
			return err
		}
		if event != "" {
			if err = alertActivity(tx, a.ID, event, message, "", now); err != nil {
				return err
			}
		}
		states[c.Key] = a
	}
	for key, a := range states {
		if observed[key] || a.Status == "ended" || a.Status == "resolved" {
			continue
		}
		if a.Status == "pending" {
			if _, err = tx.Exec("DELETE FROM alerts WHERE id=?", a.ID); err != nil {
				return err
			}
			continue
		}
		if a.missing == 0 || gap {
			a.missing = now
		}
		if now-a.missing >= int64(a.Condition.RecoverySeconds) {
			status := "ended"
			if a.Condition.Continuous {
				status = "resolved"
			}
			if err = endAlert(tx, &a, status, now); err != nil {
				return err
			}
		} else {
			a.LastSeen = s.Now().UTC()
			if err = saveAlert(tx, a); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("DELETE FROM alerts WHERE status IN ('ended','resolved') AND ended_at<?", s.Now().AddDate(0, 0, -90).Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM alert_job_sources WHERE created_at<? AND NOT EXISTS(SELECT 1 FROM alerts WHERE key='job:'||alert_job_sources.job_id AND status IN ('active','unknown','pending'))", s.Now().AddDate(0, 0, -90).Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('alerts_last_evaluation',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", strconv.FormatInt(now, 10)); err != nil {
		return err
	}
	return tx.Commit()
}
func endAlert(tx *sql.Tx, a *alertRow, status string, now int64) error {
	if a.Status == "pending" {
		_, err := tx.Exec("DELETE FROM alerts WHERE id=?", a.ID)
		return err
	}
	a.Status = status
	a.EndedAt = alertTime(now)
	a.LastSeen = time.Unix(now, 0).UTC()
	a.Revision++
	if err := saveAlert(tx, *a); err != nil {
		return err
	}
	message := "提醒条件已结束。"
	if status == "resolved" {
		message = "系统已确认异常恢复。"
	}
	return alertActivity(tx, a.ID, status, message, "", now)
}

func (s *Store) ListAlerts(page, size int, category, level, status, search string, unack bool) (model.AlertPage, error) {
	out := model.AlertPage{Items: []model.Alert{}, Page: page, PageSize: size}
	where := "status!='pending'"
	args := []any{}
	if category != "" {
		where += " AND json_extract(condition,'$.category')=?"
		args = append(args, category)
	}
	if level != "" {
		where += " AND json_extract(condition,'$.level')=?"
		args = append(args, level)
	}
	if status == "current" {
		where += " AND status IN ('active','unknown')"
	} else if status == "history" {
		where += " AND status IN ('resolved','ended')"
	} else if status != "" {
		where += " AND status=?"
		args = append(args, status)
	}
	if unack {
		where += " AND acknowledged=0 AND status IN ('active','unknown') AND " + alertAcknowledgementRequiredSQL
	}
	if search != "" {
		where += " AND (instr(lower(json_extract(condition,'$.title')),lower(?))>0 OR instr(lower(json_extract(condition,'$.message')),lower(?))>0 OR instr(id,?)>0)"
		args = append(args, search, search, search)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM alerts WHERE "+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.DB.Query("SELECT "+alertColumns+" FROM alerts WHERE "+where+" ORDER BY CASE WHEN status IN ('active','unknown') THEN 0 ELSE 1 END,CASE WHEN acknowledged=0 AND "+alertAcknowledgementRequiredSQL+" THEN 0 ELSE 1 END,CASE WHEN json_extract(condition,'$.level')='danger' THEN 0 ELSE 1 END,first_seen DESC,id LIMIT ? OFFSET ?", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		a, e := readAlert(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, a.Alert)
	}
	return out, rows.Err()
}
func (s *Store) AlertSummary() (model.AlertSummary, error) {
	var v model.AlertSummary
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(status='active'),0),COALESCE(SUM(status IN ('active','unknown') AND acknowledged=0 AND `+alertAcknowledgementRequiredSQL+`),0),COALESCE(SUM(status='unknown'),0),COALESCE(SUM(status='active' AND json_extract(condition,'$.level')='danger'),0) FROM alerts`).Scan(&v.Active, &v.Unacknowledged, &v.Unknown, &v.Critical)
	if err != nil {
		return v, err
	}
	var raw string
	err = s.DB.QueryRow("SELECT value FROM metadata WHERE key='alerts_last_evaluation'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	v.LastEvaluation = alertTime(n)
	return v, err
}
func (s *Store) Alert(id string) (model.Alert, error) {
	a, err := readAlert(s.DB.QueryRow("SELECT "+alertColumns+" FROM alerts WHERE id=? AND status!='pending'", id))
	if err != nil {
		return a.Alert, notFound(err)
	}
	rows, err := s.DB.Query("SELECT id,kind,message,actor,at FROM alert_activity WHERE alert_id=? AND kind!='read' ORDER BY id DESC LIMIT 200", id)
	if err != nil {
		return a.Alert, err
	}
	defer rows.Close()
	a.Activities = []model.AlertActivity{}
	for rows.Next() {
		var x model.AlertActivity
		var at int64
		if err = rows.Scan(&x.ID, &x.Kind, &x.Message, &x.Actor, &at); err != nil {
			return a.Alert, err
		}
		x.At = time.Unix(at, 0).UTC()
		a.Activities = append(a.Activities, x)
	}
	return a.Alert, rows.Err()
}
func (s *Store) UpdateAlert(id, actor string, in model.AlertUpdate) (model.Alert, error) {
	note := strings.TrimSpace(in.Note)
	if !in.Acknowledge && note == "" {
		return model.Alert{}, errors.New("请选择确认或填写处理备注")
	}
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > 2000 {
		return model.Alert{}, errors.New("处理备注不能超过 2000 个字符")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Alert{}, err
	}
	defer tx.Rollback()
	a, err := readAlert(tx.QueryRow("SELECT "+alertColumns+" FROM alerts WHERE id=? AND status!='pending'", id))
	if err != nil {
		return model.Alert{}, notFound(err)
	}
	if a.Revision != in.ExpectedRevision {
		return model.Alert{}, ErrConflict
	}
	if in.Acknowledge && !a.AcknowledgementRequired {
		return model.Alert{}, errors.New("部署失败通知无需手动确认")
	}
	now := s.Now().Unix()
	if in.Acknowledge && !a.Acknowledged {
		a.Acknowledged = true
		a.AcknowledgedBy = actor
		a.AcknowledgedAt = alertTime(now)
		if err = alertActivity(tx, id, "acknowledged", "管理员已确认知晓。", actor, now); err != nil {
			return model.Alert{}, err
		}
	}
	if note != "" {
		if err = alertActivity(tx, id, "note", note, actor, now); err != nil {
			return model.Alert{}, err
		}
	}
	a.Revision++
	if err = saveAlert(tx, a); err != nil {
		return model.Alert{}, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'alert.update',?)", now, fmt.Sprintf("%s:%s", id, actor)); err != nil {
		return model.Alert{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Alert{}, err
	}
	return s.Alert(id)
}

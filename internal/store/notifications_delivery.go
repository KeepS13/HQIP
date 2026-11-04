package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"hash/fnv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type notificationRow struct {
	Record        model.NotificationRecord
	Revision      int64
	Refs          []notificationRef
	JobID         int64
	Claim         string
	CycleAttempts int
}

const notificationColumns = "id,category,level,title,message,detail_path,status,attempts,created_at,next_attempt,last_error,revision,refs,job_id,claim,cycle_attempts"

type notificationScanner interface{ Scan(...any) error }

func scanNotification(row notificationScanner) (notificationRow, error) {
	var r notificationRow
	var at, due int64
	var refs string
	err := row.Scan(&r.Record.ID, &r.Record.Category, &r.Record.Level, &r.Record.Title, &r.Record.Message, &r.Record.DetailPath, &r.Record.Status, &r.Record.Attempts, &at, &due, &r.Record.LastError, &r.Revision, &refs, &r.JobID, &r.Claim, &r.CycleAttempts)
	if err != nil {
		return r, err
	}
	r.Record.CreatedAt = time.Unix(at, 0).UTC()
	if due > 0 {
		t := time.Unix(due, 0).UTC()
		r.Record.NextAttempt = &t
	}
	err = json.Unmarshal([]byte(refs), &r.Refs)
	return r, err
}

func notificationEligible(tx *sql.Tx, r notificationRow, c notificationConfig, states map[string]*notificationState, now int64) (bool, bool, []notificationRef, error) {
	if r.Revision != c.Revision || (!c.Enabled && r.Record.Category != "test") || !c.Categories.Allows(r.Record.Category) {
		return false, false, nil, nil
	}
	if r.Record.Category == "test" {
		return c.Webhook != "" && c.Secret != "", true, nil, nil
	}
	if r.JobID > 0 {
		var active int
		err := tx.QueryRow(`SELECT COUNT(*) FROM jobs j JOIN nodes n ON n.id=j.node_id WHERE j.id=? AND j.state='error' AND ((j.kind='retire' AND n.lifecycle='retiring') OR (j.kind!='retire' AND n.lifecycle='active')) AND NOT EXISTS(SELECT 1 FROM node_retirements retired WHERE retired.node_id=n.id AND retired.stopped=1) AND NOT EXISTS(SELECT 1 FROM jobs newer WHERE newer.node_id=j.node_id AND newer.kind=j.kind AND newer.id>j.id)`, r.JobID).Scan(&active)
		return active == 1, true, nil, err
	}
	refs := []notificationRef{}
	ready := true
	for _, ref := range r.Refs {
		v := states[ref.Key]
		if v == nil || v.Condition.Removed || ref.Episode != v.Episode || !c.Categories.Allows(v.Condition.Category) {
			continue
		}
		if ref.Recovery {
			if !v.Active && v.Notified {
				refs = append(refs, ref)
				if !v.Observable || v.MissingSince == 0 || now-v.MissingSince < int64(v.Condition.RecoverySeconds) {
					ready = false
				}
			}
			continue
		}
		if !v.Active || v.MissingSince > 0 || v.Condition.Stage != ref.Stage {
			continue
		}
		refs = append(refs, ref)
		if !v.Observable || now-v.Since < int64(v.Condition.DelaySeconds) {
			ready = false
		}
	}
	return len(refs) > 0, ready, refs, nil
}

func refreshPendingNotifications(tx *sql.Tx, c notificationConfig, states map[string]*notificationState, now int64) error {
	rows, err := tx.Query("SELECT " + notificationColumns + " FROM notifications WHERE status IN ('pending','retry')")
	if err != nil {
		return err
	}
	items := []notificationRow{}
	for rows.Next() {
		r, e := scanNotification(rows)
		if e != nil {
			rows.Close()
			return e
		}
		items = append(items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range items {
		valid, _, refs, e := notificationEligible(tx, r, c, states, now)
		if e != nil {
			return e
		}
		if !valid {
			if _, err = tx.Exec("UPDATE notifications SET status='cancelled',next_attempt=0,last_error='事件已过时或通知配置已变更' WHERE id=?", r.Record.ID); err != nil {
				return err
			}
			// If a never-delivered observation briefly disappeared during its
			// recovery grace, allow a later fresh incident to be enqueued again.
			for _, ref := range r.Refs {
				v := states[ref.Key]
				if v != nil && !v.Notified && v.Episode == ref.Episode && v.Condition.Stage == ref.Stage && v.MissingSince > 0 {
					v.MaxStage = 0
					if err = saveNotificationState(tx, v); err != nil {
						return err
					}
				}
			}
			continue
		}
		if r.Record.Category == "summary" || r.Record.Category == "recovery-summary" {
			condition := summaryCondition(refs, states)
			if r.Record.Category == "recovery-summary" {
				condition.Message = "控制服务监控中断至少 120 秒后已恢复，以下为重新核对的当前状态。\n" + condition.Message
			}
			if _, err = tx.Exec("UPDATE notifications SET message=?,refs=? WHERE id=?", condition.Message, jsonString(refs), r.Record.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ClaimNotificationBatch(limit int) (*model.NotificationBatch, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 20 {
		limit = 20
	}
	now := s.Now()
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return nil, err
	}
	if now.UnixMilli()-c.LastDispatch < 1000 {
		return nil, nil
	}
	if _, err = tx.Exec("UPDATE notifications SET status=CASE WHEN cycle_attempts>=6 THEN 'failed' ELSE 'retry' END,claim='',claim_until=0,last_error='发送进程中断，可能已被飞书接收',next_attempt=CASE WHEN cycle_attempts>=6 THEN 0 ELSE ? END WHERE status='sending' AND claim_until<=?", now.Add(30*time.Second).Unix(), now.Unix()); err != nil {
		return nil, err
	}
	states, err := loadNotificationStates(tx)
	if err != nil {
		return nil, err
	}
	if err = refreshPendingNotifications(tx, c, states, now.Unix()); err != nil {
		return nil, err
	}
	rows, err := tx.Query("SELECT "+notificationColumns+" FROM notifications WHERE status IN ('pending','retry') AND next_attempt<=? ORDER BY created_at,id LIMIT ?", now.Unix(), NotificationQueueLimit)
	if err != nil {
		return nil, err
	}
	items := []notificationRow{}
	for rows.Next() {
		r, e := scanNotification(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	batch := &model.NotificationBatch{Claim: NewID(), Revision: c.Revision}
	for _, r := range items {
		valid, ready, _, e := notificationEligible(tx, r, c, states, now.Unix())
		if e != nil {
			return nil, e
		}
		if !valid || !ready {
			continue
		}
		if len(batch.Records) >= limit {
			break
		}
		if _, err = tx.Exec("UPDATE notifications SET status='sending',claim=?,claim_until=?,attempts=attempts+1,cycle_attempts=cycle_attempts+1,next_attempt=0 WHERE id=?", batch.Claim, now.Add(30*time.Second).Unix(), r.Record.ID); err != nil {
			return nil, err
		}
		r.Record.Status = "sending"
		r.Record.Attempts++
		r.Record.NextAttempt = nil
		batch.Records = append(batch.Records, r.Record)
	}
	if len(batch.Records) == 0 {
		return nil, tx.Commit()
	}
	batch.Webhook, err = s.Vault.Decrypt(c.Webhook)
	if err != nil {
		return nil, err
	}
	batch.Secret, err = s.Vault.Decrypt(c.Secret)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec("UPDATE feishu_settings SET last_dispatch=? WHERE id=1", now.UnixMilli()); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return batch, nil
}

func notificationRetryDelay(id string, attempt int) time.Duration {
	delays := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(delays) {
		attempt = len(delays)
	}
	d := delays[attempt-1]
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	jitter := time.Duration(h.Sum32()%1000) * d / 10000
	return d + jitter
}

// safeError is a fixed diagnostic from the sender, never the Webhook, secret,
// upstream response body, network URL or raw application log.
func (s *Store) CompleteNotificationBatch(batch *model.NotificationBatch, accepted, retryable bool, safeError string) error {
	if batch == nil {
		return nil
	}
	if len(safeError) > 300 {
		safeError = safeError[:300]
	}
	// Defensive secret removal even if a future sender accidentally includes it.
	for _, secret := range []string{batch.Webhook, batch.Secret} {
		if secret != "" {
			safeError = strings.ReplaceAll(safeError, secret, "[已隐藏]")
		}
	}
	now := s.Now()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return err
	}
	if c.Revision != batch.Revision {
		return nil
	}
	states, err := loadNotificationStates(tx)
	if err != nil {
		return err
	}
	updated := false
	for _, record := range batch.Records {
		r, e := scanNotification(tx.QueryRow("SELECT "+notificationColumns+" FROM notifications WHERE id=? AND status='sending' AND claim=? AND revision=?", record.ID, batch.Claim, batch.Revision))
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		status := "failed"
		var due int64
		if accepted {
			status = "sent"
		} else if retryable && r.CycleAttempts < 6 {
			status = "retry"
			due = now.Add(notificationRetryDelay(r.Record.ID, r.CycleAttempts)).Unix()
		}
		message := safeError
		if accepted {
			message = ""
		}
		if _, err = tx.Exec("UPDATE notifications SET status=?,next_attempt=?,last_error=?,claim='',claim_until=0 WHERE id=? AND claim=?", status, due, message, r.Record.ID, batch.Claim); err != nil {
			return err
		}
		updated = true
		if accepted {
			for _, ref := range r.Refs {
				v := states[ref.Key]
				if v == nil || v.Episode != ref.Episode {
					continue
				}
				if !ref.Recovery {
					v.Notified = true
					v.LastSent = now.Unix()
				}
				if err = saveNotificationState(tx, v); err != nil {
					return err
				}
			}
		}
	}
	if updated {
		if accepted {
			_, err = tx.Exec("UPDATE feishu_settings SET last_success=?,last_error='' WHERE id=1", now.Unix())
		} else {
			_, err = tx.Exec("UPDATE feishu_settings SET last_error=? WHERE id=1", safeError)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) QueueFeishuTest() (model.NotificationRecord, error) {
	var out model.NotificationRecord
	tx, err := s.DB.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return out, err
	}
	if c.Webhook == "" || c.Secret == "" {
		return out, errors.New("请先保存 Webhook 地址和签名密钥")
	}
	now := s.Now().Unix()
	if c.LastTest > 0 && now-c.LastTest < 30 {
		return out, ErrNotificationRateLimited
	}
	id, err := queueNotification(tx, c, now, model.NotificationCondition{Category: "test", Level: "info", Title: "HQIP 飞书机器人测试", Message: "这是一条由管理员主动发送的测试消息，用于检查 Webhook 和签名配置。", DetailPath: "/admin/notifications"}, nil, 0)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec("UPDATE feishu_settings SET last_test=? WHERE id=1", now); err != nil {
		return out, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'feishu.test',?)", now, id); err != nil {
		return out, err
	}
	r, err := scanNotification(tx.QueryRow("SELECT "+notificationColumns+" FROM notifications WHERE id=?", id))
	if err != nil {
		return out, err
	}
	return r.Record, tx.Commit()
}

func (s *Store) Notifications(page, pageSize int) (model.NotificationPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	out := model.NotificationPage{Items: []model.NotificationRecord{}, Page: page, PageSize: pageSize}
	tx, err := s.DB.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return out, err
	}
	states, err := loadNotificationStates(tx)
	if err != nil {
		return out, err
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.Query("SELECT "+notificationColumns+" FROM notifications ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", pageSize, (page-1)*pageSize)
	if err != nil {
		return out, err
	}
	items := []notificationRow{}
	for rows.Next() {
		r, e := scanNotification(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		items = append(items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, r := range items {
		valid, ready, _, e := notificationEligible(tx, r, c, states, s.Now().Unix())
		if e != nil {
			return out, e
		}
		r.Record.CanRetry = r.Record.Status == "failed" && valid && ready
		out.Items = append(out.Items, r.Record)
	}
	return out, tx.Commit()
}

func (s *Store) RetryNotification(id string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := readNotificationConfig(tx)
	if err != nil {
		return err
	}
	states, err := loadNotificationStates(tx)
	if err != nil {
		return err
	}
	r, err := scanNotification(tx.QueryRow("SELECT "+notificationColumns+" FROM notifications WHERE id=?", id))
	if err != nil {
		return notFound(err)
	}
	valid, ready, _, err := notificationEligible(tx, r, c, states, s.Now().Unix())
	if err != nil {
		return err
	}
	if r.Record.Status != "failed" || !valid || !ready {
		return errors.New("该通知已过时或当前不能重试")
	}
	var lastRetry int64
	if err = tx.QueryRow("SELECT last_retry FROM notifications WHERE id=?", id).Scan(&lastRetry); err != nil {
		return err
	}
	if lastRetry > 0 && s.Now().Unix()-lastRetry < 30 {
		return ErrNotificationRateLimited
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM notifications WHERE status IN ('pending','retry','sending')").Scan(&count); err != nil {
		return err
	}
	if count >= NotificationQueueLimit {
		return ErrNotificationQueueFull
	}
	if _, err = tx.Exec("UPDATE notifications SET status='pending',cycle_attempts=0,next_attempt=?,last_retry=?,last_error='' WHERE id=?", s.Now().Unix(), s.Now().Unix(), id); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'feishu.retry',?)", s.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

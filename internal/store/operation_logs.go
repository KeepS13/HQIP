package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/KeepS13/HQIP/internal/model"
)

var operationLabel = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,160}$`)
var operationSecretValue = regexp.MustCompile(`(?i)(https?://|bearer\s|-----BEGIN|password\s*[=:]|secret\s*[=:]|token\s*[=:]|authorization\s*[=:])`)

// Only deliberately named metadata can enter the detail document. In particular,
// accepting an arbitrary map here must never turn a request body into a log.
var operationDetailKeys = map[string]bool{
	"fields": true, "changedFields": true, "nodeIds": true, "resourceIds": true,
	"nodeId": true, "serverId": true, "subscriptionId": true, "orderId": true,
	"sourceSHA": true, "expectedRevision": true, "revision": true,
	"succeeded": true, "failed": true, "total": true, "stage": true,
	"errorCode": true, "reasonCode": true, "status": true, "kind": true,
	"filename": true, "bytes": true, "sizeBytes": true, "sha256": true,
	"retained": true, "removed": true, "retainedCount": true, "removedCount": true,
	"scheduledAt": true, "finishedAt": true, "result": true,
	"attempts": true, "blockedUntil": true,
	"reason": true, "source": true, "banSeconds": true, "failureCount": true,
	"retryAfter": true, "banLevel": true, "banStarted": true,
	"acceptedCount": true, "failedCount": true, "totalCount": true,
	"jobIds": true, "attempt": true, "deletedCount": true,
}

func operationText(value string, limit int) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

func operationDetails(details map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range details {
		if !operationDetailKeys[key] {
			continue
		}
		switch v := value.(type) {
		case string:
			if !operationSecretValue.MatchString(v) {
				out[key] = operationText(v, 200)
			}
		case bool, int, int64, uint, uint64, float64, json.Number:
			out[key] = v
		case []string:
			clean := []string{}
			for _, item := range v {
				if len(clean) == 100 {
					break
				}
				if !operationSecretValue.MatchString(item) {
					clean = append(clean, operationText(item, 100))
				}
			}
			out[key] = clean
		case []any:
			clean := []string{}
			for _, item := range v {
				if len(clean) == 100 {
					break
				}
				if text, ok := item.(string); ok && !operationSecretValue.MatchString(text) {
					clean = append(clean, operationText(text, 100))
				}
			}
			out[key] = clean
		}
	}
	return out
}

func normalizeOperationLog(entry model.OperationLog, now time.Time) (model.OperationLog, error) {
	if entry.ID == "" {
		entry.ID = NewID()
	}
	if !operationLabel.MatchString(entry.ID) || !operationLabel.MatchString(entry.Action) {
		return entry, errors.New("操作日志标识或动作无效")
	}
	for _, value := range []string{entry.Category, entry.ActorType, entry.AuthType, entry.ResourceType, entry.RequestID, entry.JobID} {
		if value != "" && !operationLabel.MatchString(value) {
			return entry, errors.New("操作日志元数据无效")
		}
	}
	switch entry.Result {
	case "success", "failure", "denied", "accepted", "partial":
	default:
		return entry, errors.New("操作日志结果无效")
	}
	if entry.Status != 0 && (entry.Status < 100 || entry.Status > 599) {
		return entry, errors.New("操作日志响应状态无效")
	}
	if entry.At.IsZero() {
		entry.At = now
	}
	if entry.LastAt.IsZero() || entry.LastAt.Before(entry.At) {
		entry.LastAt = entry.At
	}
	entry.At, entry.LastAt = entry.At.UTC(), entry.LastAt.UTC()
	entry.ActorID = operationText(entry.ActorID, 160)
	entry.ActorName = operationText(entry.ActorName, 254)
	entry.ResourceID = operationText(entry.ResourceID, 160)
	entry.Method = strings.ToUpper(operationText(entry.Method, 12))
	entry.Route = operationText(entry.Route, 256)
	if strings.ContainsAny(entry.Route, "?#") || operationSecretValue.MatchString(entry.Route) || strings.HasPrefix(entry.Route, "/sub/") || strings.HasPrefix(entry.Route, "/usub/") {
		entry.Route = ""
	}
	if entry.IP != "" {
		ip, err := netip.ParseAddr(entry.IP)
		if err != nil || ip.Zone() != "" {
			entry.IP = ""
		} else {
			entry.IP = ip.Unmap().String()
		}
	}
	if entry.DurationMS < 0 {
		entry.DurationMS = 0
	}
	if entry.Count < 1 {
		entry.Count = 1
	}
	if entry.Count > 1000000 {
		entry.Count = 1000000
	}
	entry.Details = operationDetails(entry.Details)
	return entry, nil
}

const operationLogColumns = "id,at_ms,last_at_ms,category,action,actor_type,actor_id,actor_name,auth_type,resource_type,resource_id,method,route,ip,status,result,duration_ms,request_id,job_id,details,count"

type operationLogExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendOperationLog(ctx context.Context, db operationLogExecutor, entry model.OperationLog, now time.Time, aggregate bool) error {
	entry, err := normalizeOperationLog(entry, now)
	if err != nil {
		return err
	}
	details, err := json.Marshal(entry.Details)
	if err != nil || len(details) > 16<<10 {
		return errors.New("操作日志详情无效或过长")
	}
	query := "INSERT INTO operation_logs(" + operationLogColumns + ") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING"
	if aggregate {
		query = strings.TrimSuffix(query, "DO NOTHING") + "DO UPDATE SET count=operation_logs.count+excluded.count,last_at_ms=CASE WHEN excluded.last_at_ms>operation_logs.last_at_ms THEN excluded.last_at_ms ELSE operation_logs.last_at_ms END WHERE operation_logs.action=excluded.action AND operation_logs.result=excluded.result AND operation_logs.actor_id=excluded.actor_id AND (operation_logs.ip=excluded.ip OR (COALESCE(json_extract(operation_logs.details,'$.source'),'')!='' AND json_extract(operation_logs.details,'$.source')=json_extract(excluded.details,'$.source')))"
	}
	_, err = db.ExecContext(ctx, query, entry.ID, entry.At.UnixMilli(), entry.LastAt.UnixMilli(), entry.Category, entry.Action, entry.ActorType, entry.ActorID, entry.ActorName, entry.AuthType, entry.ResourceType, entry.ResourceID, entry.Method, entry.Route, entry.IP, entry.Status, entry.Result, entry.DurationMS, entry.RequestID, entry.JobID, string(details), entry.Count)
	return err
}

// Explicit IDs are idempotency keys: retries never change an existing event.
func (s *Store) AppendOperationLog(ctx context.Context, entry model.OperationLog) error {
	err := appendOperationLog(ctx, s.DB, entry, s.Now(), false)
	s.observeOperationLogWrite(err)
	return err
}

// AggregateOperationLog is exclusively for a bounded, deterministic security
// bucket ID. It increments Count; callers must not retry an uncertain write.
func (s *Store) AggregateOperationLog(ctx context.Context, entry model.OperationLog) error {
	err := appendOperationLog(ctx, s.DB, entry, s.Now(), true)
	s.observeOperationLogWrite(err)
	return err
}

func (s *Store) OperationLogWriteFailed() bool { return s.operationLogFailure.Load() }

// The health worker consumes this latch. Successful concurrent writes must not
// hide a lost event before the next health evaluation has observed it.
func (s *Store) TakeOperationLogWriteFailure() bool { return s.operationLogFailure.Swap(false) }

func (s *Store) observeOperationLogWrite(err error) {
	if err != nil {
		if !s.operationLogFailure.Swap(true) {
			log.Print("HQIP 操作日志写入失败；业务结果不变，请检查存储状态")
		}
	}
}

// Optional operational metadata must not roll back a completed business change.
// PostgreSQL aborts a transaction after a failed statement, so recovery must use
// a savepoint rather than simply ignoring the insert's error.
func (s *Store) bestEffortOperationLog(tx *sql.Tx, write func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := tx.ExecContext(ctx, "SAVEPOINT hqip_operation_log"); err != nil {
		s.observeOperationLogWrite(err)
		return err
	}
	err := write(ctx)
	s.observeOperationLogWrite(err)
	if err != nil {
		// Use a fresh context: a timed-out log query must not cancel recovery.
		recoverCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		if _, rollbackErr := tx.ExecContext(recoverCtx, "ROLLBACK TO SAVEPOINT hqip_operation_log"); rollbackErr != nil {
			return rollbackErr
		}
		_, err = tx.ExecContext(recoverCtx, "RELEASE SAVEPOINT hqip_operation_log")
		return err
	}
	_, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT hqip_operation_log")
	if err != nil {
		s.observeOperationLogWrite(err)
	}
	return err
}

func scanOperationLog(row interface{ Scan(...any) error }) (model.OperationLog, error) {
	var entry model.OperationLog
	var at, last int64
	var details string
	err := row.Scan(&entry.ID, &at, &last, &entry.Category, &entry.Action, &entry.ActorType, &entry.ActorID, &entry.ActorName, &entry.AuthType, &entry.ResourceType, &entry.ResourceID, &entry.Method, &entry.Route, &entry.IP, &entry.Status, &entry.Result, &entry.DurationMS, &entry.RequestID, &entry.JobID, &details, &entry.Count)
	if err != nil {
		return entry, notFound(err)
	}
	entry.At, entry.LastAt = time.UnixMilli(at).UTC(), time.UnixMilli(last).UTC()
	if err = json.Unmarshal([]byte(details), &entry.Details); err != nil {
		return entry, errors.New("操作日志详情损坏")
	}
	// Also sanitize on read so a historical or manually inserted record cannot
	// expose a payload through this API.
	entry.Details = operationDetails(entry.Details)
	return entry, nil
}

func (s *Store) OperationLog(ctx context.Context, id string) (model.OperationLog, error) {
	if !operationLabel.MatchString(id) {
		return model.OperationLog{}, ErrNotFound
	}
	return scanOperationLog(s.DB.QueryRowContext(ctx, "SELECT "+operationLogColumns+" FROM operation_logs WHERE id=?", id))
}

func (s *Store) ListOperationLogs(ctx context.Context, filter model.OperationLogFilter) (model.OperationLogPage, error) {
	page := model.OperationLogPage{Items: []model.OperationLog{}, Page: filter.Page, PageSize: filter.PageSize}
	if page.Page == 0 {
		page.Page = 1
	}
	if page.PageSize == 0 {
		page.PageSize = 20
	}
	if page.Page < 1 || page.Page > 100000 || page.PageSize < 1 || page.PageSize > 100 || (!filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To)) {
		return page, errors.New("操作日志分页或时间范围无效")
	}
	for _, value := range []string{filter.Search, filter.Actor, filter.Action, filter.IP, filter.Method, filter.AuthType, filter.Result} {
		if len(value) > 254 {
			return page, errors.New("操作日志筛选过长")
		}
	}
	where := []string{"1=1"}
	args := []any{}
	for _, part := range []struct{ column, value string }{{"action", filter.Action}, {"method", strings.ToUpper(filter.Method)}, {"auth_type", filter.AuthType}, {"result", filter.Result}} {
		if part.value != "" {
			where = append(where, part.column+"=?")
			args = append(args, part.value)
		}
	}
	if filter.IP != "" {
		ip, err := netip.ParseAddr(filter.IP)
		if err != nil || ip.Zone() != "" {
			return page, errors.New("客户端 IP 无效")
		}
		where = append(where, "ip=?")
		args = append(args, ip.Unmap().String())
	}
	like := func(value string) string {
		return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(strings.TrimSpace(value))) + "%"
	}
	if filter.Actor != "" {
		where = append(where, "(LOWER(actor_name) LIKE ? ESCAPE '!' OR LOWER(actor_id) LIKE ? ESCAPE '!')")
		args = append(args, like(filter.Actor), like(filter.Actor))
	}
	if filter.Search != "" {
		parts := []string{}
		for _, column := range []string{"id", "action", "actor_name", "route", "resource_id", "request_id", "job_id"} {
			parts = append(parts, "LOWER("+column+") LIKE ? ESCAPE '!'")
			args = append(args, like(filter.Search))
		}
		where = append(where, "("+strings.Join(parts, " OR ")+")")
	}
	if !filter.From.IsZero() {
		where = append(where, "at_ms>=?")
		args = append(args, filter.From.UnixMilli())
	}
	if !filter.To.IsZero() {
		where = append(where, "at_ms<=?")
		args = append(args, filter.To.UnixMilli())
	}
	clause := " WHERE " + strings.Join(where, " AND ")
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_logs"+clause, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	args = append(args, page.PageSize, (page.Page-1)*page.PageSize)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+operationLogColumns+" FROM operation_logs"+clause+" ORDER BY at_ms DESC,id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanOperationLog(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, entry)
	}
	return page, rows.Err()
}

// LinkOperationJob keeps the first verified initiator. Late linkage also repairs
// a fast job's already-written terminal event, without changing its outcome.
// Node jobs use "node:<numeric ID>"; server operation jobs use their normal ID.
func (s *Store) LinkOperationJob(ctx context.Context, jobID, requestID, actorType, actorID, actorName, ip string) error {
	entry, err := normalizeOperationLog(model.OperationLog{ID: jobID, JobID: jobID, RequestID: requestID, Action: "job.link", ActorType: actorType, ActorID: actorID, ActorName: actorName, IP: ip, Result: "accepted"}, s.Now())
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO operation_job_context(job_id,request_id,actor_type,actor_id,actor_name,ip,created_at) VALUES(?,?,?,?,?,?,?)", entry.JobID, entry.RequestID, entry.ActorType, entry.ActorID, entry.ActorName, entry.IP, s.Now().Unix()); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT request_id,actor_type,actor_id,actor_name,ip FROM operation_job_context WHERE job_id=?", jobID).Scan(&entry.RequestID, &entry.ActorType, &entry.ActorID, &entry.ActorName, &entry.IP); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE operation_logs SET request_id=?,actor_type=?,actor_id=?,actor_name=?,ip=? WHERE job_id=? AND request_id=''", entry.RequestID, entry.ActorType, entry.ActorID, entry.ActorName, entry.IP, jobID); err != nil {
		return err
	}
	// A successful manual check may finish before linkage and look like routine
	// polling to the worker. Materialize its terminal event from durable state.
	if !strings.HasPrefix(jobID, "node:") {
		var server, kind, state string
		var started, finished int64
		err = tx.QueryRowContext(ctx, "SELECT server_id,kind,state,started_at,finished_at FROM server_operations WHERE id=?", jobID).Scan(&server, &kind, &state, &started, &finished)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && kind == "check" && (state == "succeeded" || state == "failed") {
			entry.ID, entry.At, entry.LastAt = "server-finished:"+jobID, time.Unix(finished, 0), time.Time{}
			entry.Category, entry.Action, entry.ResourceType, entry.ResourceID = "operations", "server.check."+state, "server", server
			entry.AuthType, entry.Result = "system", "success"
			if state == "failed" {
				entry.Result = "failure"
			}
			entry.DurationMS = max(0, finished-started) * 1000
			if err = appendOperationLog(ctx, tx, entry, s.Now(), false); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func jobOperationLog(ctx context.Context, tx *sql.Tx, entry model.OperationLog, now time.Time) error {
	entry.ActorType, entry.ActorName, entry.AuthType = "system", "系统", "system"
	err := tx.QueryRowContext(ctx, "SELECT request_id,actor_type,actor_id,actor_name,ip FROM operation_job_context WHERE job_id=?", entry.JobID).Scan(&entry.RequestID, &entry.ActorType, &entry.ActorID, &entry.ActorName, &entry.IP)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return appendOperationLog(ctx, tx, entry, now, false)
}

func nodeOperationJobID(id int64) string { return fmt.Sprintf("node:%d", id) }

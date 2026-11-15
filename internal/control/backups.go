package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type backupRun struct {
	RunID         string     `json:"runId"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Status        string     `json:"status"`
	Attempt       int        `json:"attempt"`
	ErrorCode     string     `json:"errorCode,omitempty"`
	SizeBytes     int64      `json:"sizeBytes,omitempty"`
	SHA256        string     `json:"sha256,omitempty"`
	RetainedCount int        `json:"retainedCount"`
	DeletedCount  int        `json:"deletedCount"`
}
type backupStatusView struct {
	Version       int        `json:"version"`
	Enabled       bool       `json:"enabled"`
	IntervalHours int        `json:"intervalHours"`
	RetentionDays int        `json:"retentionDays"`
	NextRunAt     *time.Time `json:"nextRunAt,omitempty"`
	LastRun       *backupRun `json:"lastRun,omitempty"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
}
type backupEvent struct {
	Version      int       `json:"version"`
	EventID      string    `json:"eventId"`
	RunID        string    `json:"runId"`
	Attempt      int       `json:"attempt"`
	Action       string    `json:"action"`
	At           time.Time `json:"at"`
	Result       string    `json:"result"`
	ErrorCode    string    `json:"errorCode"`
	SizeBytes    int64     `json:"sizeBytes"`
	SHA256       string    `json:"sha256"`
	DeletedCount int       `json:"deletedCount"`
}

func readPrivateJSON(path string, dst any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errors.New("invalid status file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("status file changed")
	}
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	if err = d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid status document")
	}
	return nil
}

func (s *Server) readBackupStatus() (backupStatusView, error) {
	v := backupStatusView{Version: 1, IntervalHours: 6, RetentionDays: 14}
	err := readPrivateJSON(filepath.Join(s.Store.DataDir, "backup-status.json"), &v)
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if v.Version != 1 || v.IntervalHours != 6 || v.RetentionDays != 14 {
		return v, errors.New("invalid backup configuration")
	}
	if v.LastRun != nil {
		if v.LastRun.Status != "success" && v.LastRun.Status != "failed" && v.LastRun.Status != "running" {
			return v, errors.New("invalid backup status")
		}
		// Only fixed identifiers/codes are exposed, never arbitrary runner errors.
		if !safeOperationID(v.LastRun.RunID) || !safeStatusCode(v.LastRun.ErrorCode) {
			return v, errors.New("invalid backup receipt")
		}
	}
	return v, nil
}
func safeStatusCode(value string) bool { return value == "" || safeOperationID(value) }
func (s *Server) backupStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.readBackupStatus()
	if err != nil {
		fail(w, 503, "备份状态暂不可用，请检查备份任务")
		return
	}
	reply(w, 200, v)
}

func (s *Server) importBackupEvents(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dir := filepath.Join(s.Store.DataDir, "backup-events")
	stat, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		s.operations.failure.Store(true)
		return
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		s.operations.failure.Store(true)
		return
	}
	for i, file := range files {
		if i >= 512 || ctx.Err() != nil {
			return
		}
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") || !safeOperationID(strings.TrimSuffix(file.Name(), ".json")) {
			continue
		}
		var event backupEvent
		if err := readPrivateJSON(filepath.Join(dir, file.Name()), &event); err != nil {
			s.operations.failure.Store(true)
			continue
		}
		if event.Version != 1 || !safeOperationID(event.EventID) || !safeOperationID(event.RunID) || !safeStatusCode(event.ErrorCode) || event.At.IsZero() {
			s.operations.failure.Store(true)
			continue
		}
		if event.Action != "backup.completed" && event.Action != "backup.failed" && event.Action != "backup.pruned" {
			continue
		}
		if event.Result != "success" && event.Result != "failure" {
			continue
		}
		entry := model.OperationLog{ID: event.EventID, At: event.At, LastAt: event.At, Category: "backup", Action: event.Action, ActorType: "system", ActorName: "系统", AuthType: "system", ResourceType: "backup", ResourceID: event.RunID, Result: event.Result, JobID: event.RunID, Count: 1, Details: map[string]any{"attempt": event.Attempt, "errorCode": event.ErrorCode, "sizeBytes": event.SizeBytes, "deletedCount": event.DeletedCount}}
		s.writeOperation(ctx, entry, false)
	}
}

func (s *Server) operationHealthConditions(ctx context.Context) []model.NotificationCondition {
	observedFailure := s.operations.failure.Swap(false)
	storeFailure := s.Store.TakeOperationLogWriteFailure()
	s.flushOperationLogs(ctx)
	s.importBackupEvents(ctx)
	out := []model.NotificationCondition{}
	if observedFailure || storeFailure || s.operations.failure.Load() {
		out = append(out, model.NotificationCondition{Key: "controller:operation-logs", Category: "system", Kind: "operation-logs", ResourceID: "controller", Title: "操作日志记录异常", Message: "操作日志写入或备份回执读取失败，请检查存储与文件权限。", DetailPath: "/operation-logs", Level: "danger", Continuous: true, RecoverySeconds: 30})
	}
	v, err := s.readBackupStatus()
	if err != nil {
		out = append(out, model.NotificationCondition{Key: "controller:backup", Category: "system", Kind: "backup", ResourceID: "controller", Title: "自动备份状态异常", Message: "无法读取备份任务状态，请检查任务和权限。", DetailPath: "/operation-logs", Level: "danger", Continuous: true, RecoverySeconds: 30})
		return out
	}
	if !v.Enabled {
		return out
	}
	failed := v.LastRun != nil && v.LastRun.Status == "failed"
	stale := v.LastSuccessAt == nil || s.Store.Now().Sub(*v.LastSuccessAt) > 7*time.Hour
	if failed || stale {
		out = append(out, model.NotificationCondition{Key: "controller:backup", Category: "system", Kind: "backup", ResourceID: "controller", Title: "自动备份需要检查", Message: "最近备份失败或超过 7 小时没有成功备份，请检查备份任务和磁盘空间。", DetailPath: "/operation-logs", Level: "danger", Continuous: true, RecoverySeconds: 30})
	}
	return out
}

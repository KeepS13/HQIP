package control

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/internal/feishu"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) registerNotificationRoutes(mux *http.ServeMux) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/settings/feishu":           s.getFeishu,
		"PUT /api/v1/settings/feishu":           s.putFeishu,
		"POST /api/v1/settings/feishu/test":     s.testFeishu,
		"GET /api/v1/notifications":             s.listNotifications,
		"POST /api/v1/notifications/{id}/retry": s.retryNotification,
	} {
		mux.Handle(pattern, s.auth(handler, false))
	}
}

func (s *Server) getFeishu(w http.ResponseWriter, r *http.Request) {
	value, err := s.Store.FeishuSettings()
	result(w, value, err)
}

func notificationWriteResult(w http.ResponseWriter, value any, err error, code int) {
	if err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			fail(w, 409, "配置已被修改，请刷新后重新编辑")
		case errors.Is(err, store.ErrNotFound):
			fail(w, 404, "通知不存在")
		case errors.Is(err, store.ErrNotificationRateLimited):
			fail(w, 429, "操作过于频繁，请稍后再试")
		case errors.Is(err, store.ErrNotificationQueueFull):
			fail(w, 503, "通知队列已满，请检查投递记录后重试")
		default:
			fail(w, 400, err.Error())
		}
		return
	}
	reply(w, code, value)
}

func (s *Server) putFeishu(w http.ResponseWriter, r *http.Request) {
	var input model.FeishuSettingsInput
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if !readJSON(w, r, &input) {
		return
	}
	value, err := s.Store.SaveFeishuSettings(input)
	notificationWriteResult(w, value, err, http.StatusOK)
}

func (s *Server) testFeishu(w http.ResponseWriter, r *http.Request) {
	value, err := s.Store.QueueFeishuTest()
	notificationWriteResult(w, value, err, http.StatusAccepted)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	page, size := 1, 20
	var err error
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 100000 {
			fail(w, 400, "页码无效")
			return
		}
	}
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		size, err = strconv.Atoi(raw)
		if err != nil || size < 1 || size > 100 {
			fail(w, 400, "每页数量须为 1 至 100")
			return
		}
	}
	value, err := s.Store.Notifications(page, size)
	result(w, value, err)
}

func (s *Server) retryNotification(w http.ResponseWriter, r *http.Request) {
	// Re-evaluate before accepting a manual retry, including after downtime.
	conditions, err := s.Store.CollectNotificationConditions()
	if err == nil {
		err = s.Store.EvaluateNotifications(conditions)
	}
	if err != nil {
		fail(w, 503, "暂时无法核对通知状态，请稍后重试")
		return
	}
	err = s.Store.RetryNotification(r.PathValue("id"))
	notificationWriteResult(w, map[string]bool{"queued": true}, err, http.StatusAccepted)
}

type notificationSender interface {
	Send(context.Context, string, string, feishu.Message) feishu.Result
}

// RunNotifications is independent of SSH/ACME jobs and admission. Failed sends
// update the durable queue, never fail the controller or extend a proxy lease.
func (s *Server) RunNotifications(ctx context.Context) error {
	sender := feishu.New()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastScan, lastLog time.Time
	prepared := false
	for {
		if ctx.Err() != nil {
			return nil
		}
		var err error
		if !prepared {
			err = s.Store.PrepareNotificationRun()
			prepared = err == nil
		}
		if err == nil {
			err = s.notificationTick(ctx, sender, &lastScan)
		}
		if err != nil && (lastLog.IsZero() || time.Since(lastLog) >= time.Minute) {
			// Never log a transport URL, raw upstream response or credentials.
			log.Print("通知任务暂时无法读取或保存状态，将自动重试；请检查数据库和后台投递记录")
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Server) notificationTick(ctx context.Context, sender notificationSender, lastScan *time.Time) error {
	settings, err := s.Store.FeishuSettings()
	if err != nil {
		return err
	}
	if lastScan.IsZero() || s.Store.Now().Sub(*lastScan) >= 30*time.Second || settings.QueueDepth > 0 {
		conditions, e := s.Store.CollectNotificationConditions()
		if e != nil {
			return e
		}
		if e = s.Store.EvaluateNotifications(conditions); e != nil {
			return e
		}
		*lastScan = s.Store.Now()
	}
	batch, err := s.Store.ClaimNotificationBatch(8)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || batch == nil {
		return err
	}
	message := feishu.Message{BaseURL: s.opts.AdminOrigin}
	for _, record := range batch.Records {
		message.Events = append(message.Events, feishu.Event{ID: record.ID, Level: record.Level, Title: record.Title, Message: record.Message, DetailPath: record.DetailPath, CreatedAt: record.CreatedAt})
	}
	out := sender.Send(ctx, batch.Webhook, batch.Secret, message)
	return s.Store.CompleteNotificationBatch(batch, out.Success, out.Retryable, out.Error)
}

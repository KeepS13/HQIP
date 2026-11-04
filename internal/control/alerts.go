package control

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) registerNotificationRoutes(m *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"GET /api/v1/alerts": s.listAlerts, "GET /api/v1/alerts/summary": s.alertSummary, "GET /api/v1/alerts/{id}": s.alertDetail, "PATCH /api/v1/alerts/{id}": s.updateAlert,
	} {
		m.Handle(path, s.auth(h, false))
	}
}
func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
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
			fail(w, 400, "每页数量无效")
			return
		}
	}
	q := r.URL.Query()
	if len(q.Get("search")) > 500 {
		fail(w, 400, "搜索内容过长")
		return
	}
	v, err := s.Store.ListAlerts(page, size, q.Get("category"), q.Get("level"), q.Get("status"), q.Get("search"), q.Get("unacknowledged") == "true")
	result(w, v, err)
}
func (s *Server) alertSummary(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.AlertSummary()
	result(w, v, err)
}
func (s *Server) alertDetail(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Alert(r.PathValue("id"))
	result(w, v, err)
}
func (s *Server) updateAlert(w http.ResponseWriter, r *http.Request) {
	var in model.AlertUpdate
	if !readJSON(w, r, &in) {
		return
	}
	actor, _, _, err := s.Store.Admin()
	if err != nil {
		fail(w, 503, "管理员身份不可用")
		return
	}
	v, err := s.Store.UpdateAlert(r.PathValue("id"), actor, in)
	switch {
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "预警状态已变化，请重新读取后操作")
	case errors.Is(err, store.ErrNotFound):
		fail(w, 404, "预警记录不存在")
	case err != nil:
		fail(w, 400, "操作失败，请检查备注或刷新重试")
	default:
		reply(w, 200, v)
	}
}

// RunNotifications only evaluates local alerts. It never sends outbound messages.
func (s *Server) RunNotifications(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		conditions, err := s.Store.CollectNotificationConditions()
		if err == nil {
			err = s.Store.EvaluateAlerts(conditions)
		}
		if err != nil {
			log.Print("HQIP 预警检查失败，保留上次有效状态")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

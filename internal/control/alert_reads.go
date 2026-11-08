package control

import (
	"errors"
	"net/http"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) alertUnreadSummary(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.AlertUnreadSummary()
	result(w, v, err)
}

func (s *Server) markAlertRead(w http.ResponseWriter, r *http.Request) {
	var in model.AlertReadUpdate
	if !readJSON(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 1 {
		fail(w, 400, "预警版本无效")
		return
	}
	actor, _, _, err := s.Store.Admin()
	if err != nil {
		fail(w, 503, "管理员身份不可用")
		return
	}
	err = s.Store.MarkAlertRead(r.PathValue("id"), actor, in.ExpectedRevision)
	switch {
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "预警状态已变化，请重新读取后操作")
	case errors.Is(err, store.ErrNotFound):
		fail(w, 404, "预警记录不存在")
	case err != nil:
		fail(w, 503, "暂时无法更新阅读状态，请稍后重试")
	default:
		reply(w, 200, map[string]bool{"read": true})
	}
}

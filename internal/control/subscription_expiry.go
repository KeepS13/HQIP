package control

import (
	"errors"
	"net/http"
	"time"

	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) adminSubscriptionExpiry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpiresAt        time.Time `json:"expiresAt"`
		ExpectedRevision int64     `json:"expectedRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.ExpiresAt.IsZero() || body.ExpectedRevision < 1 {
		fail(w, 400, "请填写新的到期时间并刷新订阅版本后重试")
		return
	}
	value, err := s.Store.UpdateSubscriptionExpiry(r.PathValue("id"), r.PathValue("subscriptionId"), body.ExpiresAt, body.ExpectedRevision)
	switch {
	case err == nil:
		reply(w, 200, value)
	case errors.Is(err, store.ErrNotFound):
		fail(w, 404, "此用户的订阅不存在")
	case errors.Is(err, store.ErrSubscriptionExpired), errors.Is(err, store.ErrSubscriptionRemoved):
		fail(w, 409, err.Error())
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "订阅已发生变化，请关闭弹窗并刷新后重试")
	case errors.Is(err, store.ErrSubscriptionExpiry), errors.Is(err, store.ErrSubscriptionPeriodExpiry):
		fail(w, 400, err.Error())
	default:
		fail(w, 503, "暂时无法调整到期时间，请刷新确认当前结果后重试")
	}
}

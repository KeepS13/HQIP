package control

import (
	"errors"
	"net/http"

	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) adminSubscriptionNodes(w http.ResponseWriter, r *http.Request) {
	user, id := r.PathValue("id"), r.PathValue("subscriptionId")
	var value any
	var err error
	if r.Method == http.MethodGet {
		value, err = s.Store.SubscriptionNodes(user, id)
	} else {
		var body struct {
			NodeIDs          []string `json:"nodeIds"`
			ExpectedRevision int64    `json:"expectedRevision"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		value, err = s.Store.UpdateSubscriptionNodes(user, id, body.NodeIDs, body.ExpectedRevision)
	}
	switch {
	case err == nil:
		reply(w, 200, value)
	case errors.Is(err, store.ErrNotFound):
		fail(w, 404, "此用户的订阅不存在")
	case errors.Is(err, store.ErrSubscriptionNodes):
		fail(w, 400, err.Error())
	case errors.Is(err, store.ErrSubscriptionNodesState):
		fail(w, 409, err.Error())
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "订阅或节点分配已发生变化，请重新加载后重试。"+err.Error())
	default:
		fail(w, 503, "暂时无法处理订阅节点，请刷新确认当前结果后重试")
	}
}

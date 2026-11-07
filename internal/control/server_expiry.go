package control

import (
	"errors"
	"net/http"

	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) correctServerExpiry(w http.ResponseWriter, r *http.Request) {
	var request store.ServerExpiryRequest
	if !readJSON(w, r, &request) {
		return
	}
	actor, _, _, err := s.Store.Admin()
	if err != nil {
		result(w, nil, err)
		return
	}
	server, change, err := s.Store.CorrectServerExpiry(r.PathValue("id"), request, actor)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, 404, "服务器不存在")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, "服务器或修改请求已发生变化，请刷新后重试")
		return
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]any{"server": server, "change": change})
}

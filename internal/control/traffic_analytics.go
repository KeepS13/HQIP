package control

import (
	"errors"
	"net/http"

	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) trafficAnalytics(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	data, err := s.Store.TrafficAnalytics(r.Context(), query.Get("from"), query.Get("to"), query.Get("serverId"))
	if errors.Is(err, store.ErrTrafficRange) {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	result(w, data, err)
}

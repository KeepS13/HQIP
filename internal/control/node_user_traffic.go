package control

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/KeepS13/HQIP/internal/store"
)

// nodeUserTraffic exposes account emails only behind the administrator route.
// Defaults are 20 users per page; validation also runs at the store boundary.
func (s *Server) nodeUserTraffic(w http.ResponseWriter, r *http.Request) {
	offset, limit := 0, 20
	for name, target := range map[string]*int{"offset": &offset, "limit": &limit} {
		if values, present := r.URL.Query()[name]; present {
			if len(values) != 1 {
				fail(w, http.StatusBadRequest, store.ErrNodeUserTrafficPage.Error())
				return
			}
			value, err := strconv.Atoi(values[0])
			if err != nil {
				fail(w, http.StatusBadRequest, store.ErrNodeUserTrafficPage.Error())
				return
			}
			*target = value
		}
	}
	data, err := s.Store.NodeUserTraffic(r.Context(), r.PathValue("id"), offset, limit)
	if errors.Is(err, store.ErrNodeUserTrafficPage) {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	result(w, data, err)
}

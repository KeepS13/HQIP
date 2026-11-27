package control

import "net/http"

// Only the authenticated admin route can apply an explicitly saved revision.
func (s *Server) applySubscriptionTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	result, err := s.Store.ApplySubscriptionTemplate(r.PathValue("id"), body.ExpectedRevision)
	portalResult(w, result, err)
}

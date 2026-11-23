package control

import "net/http"

// Node identity comes exclusively from agent authentication. A check never
// issues a lease and works even when the subscription's online slots are full.
func (s *Server) agentAdmissionIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	node, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	account, err := s.Store.CheckAdmissionIdentity(r.Context(), node, body.Password)
	portalResult(w, map[string]any{"authenticated": true, "accountingId": account}, err)
}

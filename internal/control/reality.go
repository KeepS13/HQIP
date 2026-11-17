package control

import "net/http"

// agentRealityCredential never accepts a node/user selector from the body. The
// private credential is returned only to its authenticated node over HTTPS.
func (s *Server) agentRealityCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	node, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var body struct {
		UUID string `json:"uuid"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	password, err := s.Store.ResolveRealityCredential(r.Context(), node, body.UUID)
	portalResult(w, map[string]string{"password": password}, err)
}

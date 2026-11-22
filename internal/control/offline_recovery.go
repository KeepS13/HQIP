package control

import "net/http"

// Recovery uses node authentication, never a user's plaintext credential. This
// endpoint only returns a previous admission; normal reports settle its usage.
func (s *Server) agentOfflineRecovery(w http.ResponseWriter, r *http.Request) {
	node, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var body struct {
		ConnectionID string `json:"connectionId"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	grant, err := s.Store.RecoverOfflineGrant(node, body.ConnectionID)
	portalResult(w, grant, err)
}

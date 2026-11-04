package control

import (
	"github.com/KeepS13/HQIP/internal/model"
	"net/http"
)

func (s *Server) agentAdmission(w http.ResponseWriter, r *http.Request) {
	node, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var b struct {
		ConnectionID string `json:"connectionId"`
		Password     string `json:"password"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	lease, err := s.Store.AcquireLease(node, b.ConnectionID, b.Password)
	portalResult(w, lease, err)
}
func (s *Server) agentLeaseRenew(w http.ResponseWriter, r *http.Request) {
	node, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var b model.LeaseUsage
	if !readJSON(w, r, &b) {
		return
	}
	lease, err := s.Store.RenewLease(node, r.PathValue("id"), b)
	portalResult(w, lease, err)
}

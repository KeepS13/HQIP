package control

import (
	"github.com/KeepS13/HQIP/internal/model"
	"net/http"
)

func (s *Server) agentOfflineAdmission(w http.ResponseWriter, r *http.Request) {
	node, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var body struct {
		ConnectionID      string `json:"connectionId"`
		Password          string `json:"password"`
		BillingProtocol   string `json:"billingProtocol,omitempty"`
		BillingCapability string `json:"billingCapability,omitempty"`
		QuotaCapability   string `json:"quotaCapability,omitempty"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var grant model.OfflineGrant
	var err error
	if body.QuotaCapability == model.NodeQuotaCapability && body.BillingCapability == "traffic-billing-v1" {
		grant, err = s.Store.AcquireNodeQuotaGrant(node, body.ConnectionID, body.Password, body.BillingProtocol)
	} else if body.QuotaCapability != "" {
		http.Error(w, "unsupported node quota capability", http.StatusBadRequest)
		return
	} else if body.BillingCapability == "traffic-billing-v1" {
		grant, err = s.Store.AcquireProtocolOfflineGrant(node, body.ConnectionID, body.Password, body.BillingProtocol)
	} else {
		grant, err = s.Store.AcquireOfflineGrant(node, body.ConnectionID, body.Password)
	}
	portalResult(w, grant, err)
}

func (s *Server) agentOfflineReport(w http.ResponseWriter, r *http.Request) {
	node, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var body model.OfflineUsage
	if !readJSON(w, r, &body) {
		return
	}
	grant, err := s.Store.ReportOfflineGrant(node, r.PathValue("id"), body)
	portalResult(w, grant, err)
}

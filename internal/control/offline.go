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
		ClientIP          string `json:"clientIP"`
		ConnectionID      string `json:"connectionId"`
		Password          string `json:"password"`
		BillingProtocol   string `json:"billingProtocol,omitempty"`
		BillingCapability string `json:"billingCapability,omitempty"`
		QuotaCapability   string `json:"quotaCapability,omitempty"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	ip, err := model.CanonicalClientIP(body.ClientIP)
	if err != nil || body.QuotaCapability != model.NodeQuotaCapability || body.BillingCapability != "traffic-billing-v1" {
		fail(w, http.StatusBadRequest, "节点须升级以提供来源 IP 和节点额度计费")
		return
	}
	grant, err := s.Store.AcquireNodeQuotaGrant(node, body.ConnectionID, body.Password, body.BillingProtocol, ip)
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

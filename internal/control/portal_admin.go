package control

import (
	"github.com/KeepS13/HQIP/internal/model"
	"net/http"
	"time"
)

func (s *Server) portalPlans(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.Plans()
	portalResult(w, v, e)
}
func (s *Server) portalSavePlan(w http.ResponseWriter, r *http.Request) {
	var b struct {
		model.Plan
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	v, e := s.Store.SavePlan(r.PathValue("id"), b.Plan, b.ExpectedRevision)
	portalResult(w, v, e)
}
func (s *Server) portalPlanPreview(w http.ResponseWriter, r *http.Request) {
	var b struct {
		model.Plan
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	v, e := s.Store.PreviewPortalPlan(r.PathValue("id"), b.Plan)
	portalResult(w, v, e)
}
func (s *Server) portalUsers(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.PortalUsers()
	portalResult(w, v, e)
}
func (s *Server) portalAdminUser(w http.ResponseWriter, r *http.Request) {
	v, e := s.portalProfileData(r.PathValue("id"), "")
	portalResult(w, v, e)
}
func (s *Server) portalAdminEnabled(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled          bool  `json:"enabled"`
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	err := s.Store.SetPortalEnabled(r.PathValue("id"), b.Enabled, b.ExpectedRevision)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	s.portalAdminUser(w, r)
}
func (s *Server) portalAdminAssignment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PlanID           string    `json:"planId"`
		ExpiresAt        time.Time `json:"expiresAt"`
		ResetUsage       bool      `json:"resetUsage"`
		ExpectedRevision int64     `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	err := s.Store.AssignPortal(r.PathValue("id"), b.PlanID, b.ExpiresAt, b.ResetUsage, b.ExpectedRevision)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	s.portalAdminUser(w, r)
}
func (s *Server) portalAdminPassword(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	portalResult(w, map[string]bool{"ok": true}, s.Store.PortalPassword(r.PathValue("id"), "", b.Password, true))
}
func (s *Server) portalAdminRevokeSession(w http.ResponseWriter, r *http.Request) {
	portalResult(w, map[string]bool{"ok": true}, s.Store.RevokePortalSession(r.PathValue("id"), r.PathValue("sessionId")))
}
func (s *Server) portalAdminRevokeConnection(w http.ResponseWriter, r *http.Request) {
	portalResult(w, map[string]bool{"ok": true}, s.Store.RevokePortalConnection(r.PathValue("id"), r.PathValue("leaseId"), ""))
}
func (s *Server) portalActivate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Confirm bool `json:"confirm"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if !b.Confirm {
		fail(w, 400, "必须明确确认旧设备鉴权将被禁用")
		return
	}
	nodes, err := s.Store.Nodes()
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	for _, n := range nodes {
		if n.Lifecycle != "active" {
			continue
		}
		if n.LastSeen == nil || s.Store.Now().Sub(*n.LastSeen) > 30*time.Second || n.LastSeen.After(s.Store.Now()) || !n.CoreHealthy || n.AppliedRevision < n.DesiredRevision {
			fail(w, 409, "仍有节点未提供30秒内健康心跳与当前配置确认，请先检查所有活动节点")
			return
		}
		capable := false
		for _, c := range n.Capabilities {
			if c == "admission-v1" {
				capable = true
			}
		}
		if !capable {
			fail(w, 409, "仍有节点未报告中央准入能力，请先完成节点升级")
			return
		}
	}
	portalResult(w, map[string]bool{"portalEnabled": true}, s.Store.ActivatePortal())
}

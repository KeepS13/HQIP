package control

import (
	"net/http"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) subscriptionTemplates(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.SubscriptionTemplates(false)
	portalResult(w, v, err)
}

func (s *Server) saveSubscriptionTemplate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		model.SubscriptionTemplate
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	v, err := s.Store.SaveSubscriptionTemplate(r.PathValue("id"), b.SubscriptionTemplate, b.ExpectedRevision)
	if err == nil {
		s.Store.Audit("subscription.template.save", v.ID)
	}
	portalResult(w, v, err)
}

func (s *Server) previewSubscriptionTemplate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		model.SubscriptionTemplate
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	current, err := s.Store.SubscriptionTemplate(r.PathValue("id"))
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	if current.Revision != b.ExpectedRevision {
		portalResult(w, nil, store.ErrConflict)
		return
	}
	v, err := s.Store.PreviewSubscriptionTemplate(current.ID, b.SubscriptionTemplate)
	portalResult(w, v, err)
}

func (s *Server) adminSubscriptionRequests(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.SubscriptionRequests("")
	portalResult(w, v, err)
}

func (s *Server) decideSubscriptionRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedRevision int64  `json:"expectedRevision"`
		TemplateRevision int64  `json:"templateRevision"`
		Note             string `json:"note"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	action := "approve"
	if strings.HasSuffix(r.URL.Path, "/reject") {
		action = "reject"
	}
	v, err := s.Store.DecideSubscriptionRequest(r.PathValue("id"), action, b.Note, b.ExpectedRevision, b.TemplateRevision)
	if err == nil {
		s.Store.Audit("subscription.request."+action, v.ID)
	}
	portalResult(w, v, err)
}

func (s *Server) adminUserSubscriptions(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.UserSubscriptions(r.PathValue("id"))
	portalResult(w, v, err)
}

func (s *Server) adminAssignSubscription(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TemplateID       string `json:"templateId"`
		RequestID        string `json:"requestId"`
		ExpectedRevision int64  `json:"expectedRevision"`
		TemplateRevision int64  `json:"templateRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	v, err := s.Store.AssignSubscription(r.PathValue("id"), b.TemplateID, b.RequestID, b.ExpectedRevision, b.TemplateRevision)
	if err == nil {
		s.Store.Audit("subscription.assign", v.ID)
	}
	portalResult(w, v, err)
}

func (s *Server) adminRemoveSubscription(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	err := s.Store.RevokeSubscription(r.PathValue("id"), r.PathValue("subscriptionId"), b.ExpectedRevision)
	if err == nil {
		s.Store.Audit("subscription.remove", r.PathValue("subscriptionId"))
	}
	portalResult(w, map[string]bool{"ok": true}, err)
}

func (s *Server) userCatalog(w http.ResponseWriter, r *http.Request, user, session string) {
	v, err := s.Store.SubscriptionCatalog(user)
	portalResult(w, v, err)
}

func (s *Server) userSubscriptionRequests(w http.ResponseWriter, r *http.Request, user, session string) {
	if r.Method == http.MethodGet {
		v, err := s.Store.SubscriptionRequests(user)
		portalResult(w, v, err)
		return
	}
	if !s.allowLogin(w, r, "subscription-request:"+user) {
		return
	}
	var b struct {
		TemplateID       string `json:"templateId"`
		RequestID        string `json:"requestId"`
		TemplateRevision int64  `json:"templateRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	v, err := s.Store.CreateSubscriptionRequest(user, b.TemplateID, b.RequestID, b.TemplateRevision)
	if err == nil {
		s.Store.Audit("subscription.request.create", v.ID)
	}
	portalResult(w, v, err)
}

func (s *Server) cancelSubscriptionRequest(w http.ResponseWriter, r *http.Request, user, session string) {
	var b struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	err := s.Store.CancelSubscriptionRequest(user, r.PathValue("id"), b.ExpectedRevision)
	if err == nil {
		s.Store.Audit("subscription.request.cancel", r.PathValue("id"))
	}
	portalResult(w, map[string]bool{"ok": true}, err)
}

func (s *Server) userSubscriptions(w http.ResponseWriter, r *http.Request, user, session string) {
	v, err := s.Store.UserSubscriptions(user)
	portalResult(w, v, err)
}

func (s *Server) userSubscriptionDetail(w http.ResponseWriter, r *http.Request, user, session string) {
	v, err := s.Store.UserSubscription(user, r.PathValue("id"))
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	nodes := []map[string]any{}
	for _, id := range v.NodeIDs {
		n, err := s.Store.Node(id)
		if err != nil {
			continue
		}
		nodes = append(nodes, map[string]any{"id": n.ID, "name": n.Name, "server": n.PublicIP, "publicIp": n.PublicIP, "port": n.Port, "sni": n.Domain, "ready": n.Status == "ready" && n.Lifecycle == "active"})
	}
	link, err := s.Store.PortalSubscription(user, v.ID, "get")
	portalResult(w, map[string]any{"subscription": v, "nodes": nodes, "subscriptionUrl": link}, err)
}

func (s *Server) nativeSelectSubscription(w http.ResponseWriter, r *http.Request, user, session string) {
	var b struct {
		SubscriptionID   string `json:"subscriptionId"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if _, err := s.Store.SelectSubscription(user, session, b.SubscriptionID, b.ExpectedRevision); err != nil {
		portalResult(w, nil, err)
		return
	}
	// A mutation always returns the new complete profile, even if a caller sent
	// a conditional-read header belonging to its old selection.
	r.Header.Del("If-None-Match")
	s.nativeProfile(w, r, user, session)
}

package control

import (
	"errors"
	"log"
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
	if b.TemplateID == "" || len(b.RequestID) < 8 || len(b.RequestID) > 128 || strings.ContainsAny(b.RequestID, "\r\n\x00") {
		reply(w, http.StatusBadRequest, map[string]string{"code": "SUBSCRIPTION_ASSIGNMENT_REJECTED", "error": "开通请求缺少有效模板或请求标识，请刷新后重试"})
		return
	}
	v, err := s.Store.AssignSubscription(r.PathValue("id"), b.TemplateID, b.RequestID, b.ExpectedRevision, b.TemplateRevision)
	if err == nil {
		reply(w, http.StatusOK, v)
		return
	}
	status := 0
	if !errors.Is(err, store.ErrSubscriptionAssignmentUnconfirmed) {
		switch {
		case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrSubscriptionStock):
			status = http.StatusConflict
		case errors.Is(err, store.ErrEntitlement):
			status = http.StatusForbidden
		case errors.Is(err, store.ErrNotFound):
			status = http.StatusNotFound
		}
	}
	if status == 0 {
		// Do not expose database parameters or call an uncertain commit a failed
		// grant. The caller must retain its key to recover the same subscription.
		log.Printf("HQIP 手动订阅开通暂未确认 (%s)", databaseFailureClass(err))
		w.Header().Set("Retry-After", "1")
		reply(w, http.StatusServiceUnavailable, map[string]string{"code": "SUBSCRIPTION_ASSIGNMENT_UNAVAILABLE", "error": "开通结果暂未确认，请重试本次开通；不会重复赠送"})
		return
	}
	// Only this explicit business rejection permits a client to discard its
	// pending key. Authentication/CSRF errors can precede a replay of a grant
	// whose first response was lost, so HTTP status alone is insufficient.
	reply(w, status, map[string]string{"code": "SUBSCRIPTION_ASSIGNMENT_REJECTED", "error": err.Error()})
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
		name := n.Name
		endpoint, err := s.Store.RealityEndpoint(n.ID)
		if err != nil {
			portalResult(w, nil, err)
			return
		}
		if endpoint != nil {
			name += " · Hysteria 2"
		}
		nodes = append(nodes, map[string]any{"id": n.ID, "name": name, "protocol": "hysteria2", "nodeId": n.ID, "server": n.Domain, "publicIp": n.PublicIP, "port": n.Port, "sni": n.Domain, "ready": n.Status == "ready" && n.Lifecycle == "active"})
		if endpoint != nil {
			nodes = append(nodes, map[string]any{"id": n.ID + ":vless", "name": n.Name + " · VLESS + Reality", "protocol": "vless", "nodeId": n.ID, "server": n.Domain, "publicIp": n.PublicIP, "port": endpoint.Port, "sni": endpoint.ServerName, "ready": n.Status == "ready" && n.Lifecycle == "active"})
		}
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

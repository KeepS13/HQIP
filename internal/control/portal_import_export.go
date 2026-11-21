package control

import (
	"mime"
	"net/http"
	"slices"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
	"gopkg.in/yaml.v3"
)

// Combined exports retain each subscription's credentials and quota. Prefixing
// node IDs/names prevents identical servers and template copies from colliding.
func (s *Server) portalImportProfile(user string, subscriptions []string) (model.ClientProfile, error) {
	combined := model.ClientProfile{Nodes: []model.ClientNode{}}
	active := false
	for _, id := range subscriptions {
		profile, subscription, err := s.Store.PortalClientProfile(user, "subscription", id)
		if err != nil {
			return combined, err
		}
		if subscription == nil || subscription.Status != "active" {
			continue
		}
		active = true
		if len(subscriptions) == 1 {
			return profile, nil
		}
		if profile.DefaultNodeID == "" {
			continue
		}
		for _, node := range profile.Nodes {
			if !node.Ready {
				continue
			}
			originalID := node.ID
			node.ID = id + ":" + node.ID
			node.Name = subscription.TemplateName + " #" + id + " · " + node.Name
			if combined.DefaultNodeID == "" && originalID == profile.DefaultNodeID {
				combined.DefaultNodeID = node.ID
			}
			combined.Nodes = append(combined.Nodes, node)
		}
	}
	if !active {
		return combined, store.ErrEntitlement
	}
	return combined, nil
}

func (s *Server) portalYAML(w http.ResponseWriter, r *http.Request) {
	format, err := portalExportFormat(r.URL.RawQuery)
	if err != nil {
		fail(w, 400, "不支持的订阅格式")
		return
	}
	if !s.Store.PortalEnabled() {
		fail(w, 404, "订阅不可用")
		return
	}
	user, subscriptions, err := s.Store.ResolvePortalImport(r.PathValue("token"))
	if err != nil {
		fail(w, 404, "订阅不存在或已撤销")
		return
	}
	p, err := s.portalImportProfile(user, subscriptions)
	if err != nil {
		if _, _, tokenErr := s.Store.ResolvePortalImport(r.PathValue("token")); tokenErr != nil {
			fail(w, 404, "订阅不存在或已撤销")
			return
		}
		portalResult(w, nil, err)
		return
	}
	proxies := []map[string]any{}
	names := []string{}
	defaultReady := false
	for _, n := range p.Nodes {
		if !n.Ready {
			continue
		}
		name := n.Name + " · " + n.ID
		proxy, proxyErr := n.ClashProxy(name)
		if proxyErr != nil {
			portalResult(w, nil, proxyErr)
			return
		}
		if n.ID == p.DefaultNodeID {
			defaultReady = true
			names = append([]string{name}, names...)
			proxies = append([]map[string]any{proxy}, proxies...)
		} else {
			names = append(names, name)
			proxies = append(proxies, proxy)
		}
	}
	if !defaultReady {
		fail(w, 503, errPortalDefaultNodeUnavailable.Error())
		return
	}
	var body []byte
	contentType := "text/yaml; charset=utf-8"
	if format == "v2ray" {
		body, err = portalV2RaySubscription(p)
		contentType = "text/plain; charset=utf-8"
	} else if format == "singbox" {
		v, rulesErr := s.Store.Rules()
		if rulesErr != nil {
			portalResult(w, nil, rulesErr)
			return
		}
		body, err = portalSingBoxSubscription(p, v.Bundle, s.opts.PublicURL, s.opts.UserOrigin)
		contentType = "application/json; charset=utf-8"
	} else {
		v, rulesErr := s.Store.Rules()
		if rulesErr != nil {
			portalResult(w, nil, rulesErr)
			return
		}
		rules, rulesErr := v.Bundle.ClashRules("节点选择", "DIRECT")
		if rulesErr != nil {
			portalResult(w, nil, rulesErr)
			return
		}
		groups := []map[string]any{}
		groups = append([]map[string]any{{"name": "节点选择", "type": "select", "proxies": names}}, groups...)
		body, err = yaml.Marshal(map[string]any{"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "warning", "proxies": proxies, "proxy-groups": groups, "rules": rules})
	}
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	// Profile construction can span a subscription rotation. Never deliver
	// replacement credentials to a request authenticated by the previous URL.
	current, currentSubscriptions, err := s.Store.ResolvePortalImport(r.PathValue("token"))
	if err != nil || current != user || !slices.Equal(currentSubscriptions, subscriptions) {
		fail(w, 404, "订阅不存在或已撤销")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Profile-Update-Interval", "1")
	if r.URL.Query().Get("download") == "1" {
		extension := map[string]string{"clash": "yaml", "v2ray": "txt", "singbox": "json"}[format]
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "hqip-" + format + "." + extension}))
	}
	_, _ = w.Write(body)
}

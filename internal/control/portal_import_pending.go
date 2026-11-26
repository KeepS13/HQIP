package control

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"slices"

	"github.com/KeepS13/HQIP/internal/rules"
	"gopkg.in/yaml.v3"
)

var errPortalActivationPending = errors.New("订阅待管理员分配节点并开通")

// A pending import is a valid capability but contains no proxy credentials.
// Only recovery websites can go direct while waiting; all other traffic remains
// rejected. No proxy credentials or authorization are granted. Serialization
// errors propagate to the HTTP handler rather than serving a partial profile.
func pendingPortalConfig(format string, serviceURLs ...string) ([]byte, string, error) {
	bundle := rules.Bundle{SchemaVersion: rules.SchemaVersion,
		Rules: append(rules.ServiceBypassRules(serviceURLs...), rules.Rule{Type: "MATCH", Outbound: "reject"})}
	switch format {
	case "v2ray":
		return []byte{}, "text/plain; charset=utf-8", nil // Base64 of an empty URI list.
	case "singbox":
		rows, err := bundle.RouteRules("proxy", "direct", "")
		if err != nil {
			return nil, "", err
		}
		body, err := json.Marshal(map[string]any{
			"inbounds":  []map[string]any{{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890}},
			"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
			"dns":       map[string]any{"servers": []map[string]any{{"type": "local", "tag": "local"}}, "final": "local"},
			"route":     map[string]any{"rules": rows, "default_domain_resolver": "local"},
		})
		return body, "application/json; charset=utf-8", err
	default:
		rows, err := bundle.ClashRules("节点选择", "DIRECT")
		if err != nil {
			return nil, "", err
		}
		body, err := yaml.Marshal(map[string]any{
			"mixed-port": 7890, "allow-lan": false, "mode": "rule", "dns": clashDNS(bundle),
			"proxies": []any{}, "proxy-groups": []map[string]any{{"name": "节点选择", "type": "select", "proxies": []string{"REJECT"}}},
			"rules": rows,
		})
		return body, "text/yaml; charset=utf-8", err
	}
}

// Revalidate capabilities after profile construction, including empty exports.
// A rotation racing this request must never receive replacement credentials.
func (s *Server) writePortalImport(w http.ResponseWriter, r *http.Request, user string, subscriptions []string, format string, body []byte, contentType string) {
	current, ids, err := s.Store.ResolvePortalImport(r.PathValue("token"))
	if err != nil || current != user || !slices.Equal(ids, subscriptions) {
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

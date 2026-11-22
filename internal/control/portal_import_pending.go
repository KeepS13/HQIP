package control

import (
	"errors"
	"mime"
	"net/http"
	"slices"
)

var errPortalActivationPending = errors.New("订阅待管理员分配节点并开通")

// A pending import is a valid capability but contains no proxy credentials.
// Config formats explicitly reject traffic while waiting; they must not route
// through another subscription or silently turn a custom plan into DIRECT.
func pendingPortalConfig(format string) ([]byte, string) {
	switch format {
	case "v2ray":
		return []byte{}, "text/plain; charset=utf-8" // Base64 of an empty URI list.
	case "singbox":
		return []byte(`{"inbounds":[{"type":"mixed","tag":"mixed-in","listen":"127.0.0.1","listen_port":7890}],"outbounds":[],"route":{"rules":[{"action":"reject"}]}}`), "application/json; charset=utf-8"
	default:
		return []byte("# 待管理员分配节点并开通，之后刷新同一订阅链接\nmixed-port: 7890\nallow-lan: false\nmode: rule\nproxies: []\nproxy-groups:\n  - name: 节点选择\n    type: select\n    proxies: [REJECT]\nrules: ['MATCH,REJECT']\n"), "text/yaml; charset=utf-8"
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

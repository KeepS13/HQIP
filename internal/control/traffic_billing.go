package control

import (
	"github.com/KeepS13/HQIP/internal/model"
	"net/http"
)

func (s *Server) trafficBillingSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var input model.TrafficBillingInput
		if !readJSON(w, r, &input) {
			return
		}
		value, err := s.Store.SaveTrafficBillingSettings(input, s.paymentActor())
		portalResult(w, value, err)
		return
	}
	value, err := s.Store.TrafficBillingSettings()
	portalResult(w, value, err)
}

func (s *Server) nativeBilling(w http.ResponseWriter, r *http.Request, user, session string) {
	w.Header().Set("Cache-Control", "no-store")
	node, protocol := r.URL.Query().Get("nodeId"), r.URL.Query().Get("protocol")
	if node == "" || (protocol != "hysteria2" && protocol != "vless") {
		fail(w, 400, "节点或协议无效")
		return
	}
	value, err := s.Store.BillingDisplay(user, session, node, protocol)
	portalResult(w, value, err)
}

package control

import "net/http"

func (s *Server) nodeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := s.Store.Node(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	events, err := s.Store.Events(id, 0)
	if err != nil {
		result(w, nil, err)
		return
	}
	traffic, err := s.Store.Traffic(id, "", r.URL.Query().Get("period"))
	if err != nil {
		result(w, nil, err)
		return
	}
	devices, err := s.Store.NodeDevices(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	n.HostNetwork, err = s.Store.HostNetwork(n.ServerID)
	if err != nil {
		result(w, nil, err)
		return
	}
	reply(w, 200, map[string]any{"node": n, "events": events, "traffic": traffic, "devices": devices})
}

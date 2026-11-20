package control

import "net/http"

func (s *Server) switchNodePublicIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OldPublicIP            string `json:"oldPublicIp"`
		PublicIP               string `json:"publicIp"`
		Password               string `json:"password"`
		ExpectedRevision       int64  `json:"expectedRevision"`
		ExpectedServerRevision int64  `json:"expectedServerRevision"`
		ManualDNSConfirmed     bool   `json:"manualDnsConfirmed"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Password == "" {
		var err error
		body.Password, err = s.savedNodePassword(r.PathValue("id"))
		if err != nil {
			managementError(w, err)
			return
		}
	}
	node, err := s.Store.QueueNodeIPSwitch(r.PathValue("id"), body.OldPublicIP, body.PublicIP, body.Password, body.ExpectedRevision, body.ExpectedServerRevision, body.ManualDNSConfirmed)
	if err != nil {
		managementError(w, err)
		return
	}
	s.linkNodeOperationJob(r, node.ID, node.QueuedJobID)
	s.signal()
	reply(w, http.StatusAccepted, node)
}

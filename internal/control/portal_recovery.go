package control

import (
	"github.com/KeepS13/HQIP/internal/store"
	"net/http"
)

func (s *Server) portalRecovery(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.PortalRecovery()
	portalResult(w, v, e)
}
func (s *Server) portalResume(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RecoveryEpoch   string                `json:"recoveryEpoch"`
		LedgerConfirmed bool                  `json:"ledgerConfirmed"`
		Users           []store.RecoveryTotal `json:"users"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	portalResult(w, map[string]bool{"ok": true}, s.Store.ResumePortalRecovery(b.RecoveryEpoch, b.LedgerConfirmed, b.Users))
}

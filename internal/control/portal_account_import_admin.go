package control

import "net/http"

// Bulk preparation is an explicit authenticated administrator operation. Return
// counts only; users retrieve their own capability through the user endpoint.
func (s *Server) prepareAccountImportLinks(w http.ResponseWriter, r *http.Request) {
	result, err := s.Store.PreparePortalAccountImports(r.Context())
	portalResult(w, result, err)
}

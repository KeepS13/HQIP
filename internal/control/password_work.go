package control

import "net/http"

// Password changes/resets need the same CPU/memory budget as login. A valid
// account must not be able to launch unbounded concurrent Argon2 computations.
func (s *Server) beginPasswordWork(w http.ResponseWriter, r *http.Request, subject string) bool {
	if !s.allowLogin(w, r, "password-work:"+subject) {
		return false
	}
	select {
	case s.authSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		fail(w, http.StatusServiceUnavailable, "密码校验繁忙，请稍后重试")
		return false
	}
}

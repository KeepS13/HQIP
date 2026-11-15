package control

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/internal/ratelimit"
)

type adminLoginProtection = ratelimit.AdminProtection

func (s *Server) adminLoginProtector() (adminLoginProtection, error) {
	if s.opts.LoginLimiter != nil {
		guard, ok := s.opts.LoginLimiter.(adminLoginProtection)
		if !ok {
			return nil, errors.New("共享管理员登录防护不可用")
		}
		return guard, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adminLoginLocal == nil {
		s.adminLoginLocal = ratelimit.NewMemoryAdminProtection(func() time.Time { return s.Store.Now() })
	}
	return s.adminLoginLocal, nil
}

func adminLoginUnavailable(w http.ResponseWriter, r *http.Request) {
	setOperationOutcome(r, "admin.login", "failure", map[string]any{"reason": "protection_unavailable"})
	w.Header().Set("Retry-After", "5")
	fail(w, http.StatusServiceUnavailable, "安全校验服务暂不可用，请稍后重试")
}

func adminLoginDetails(ip string, decision ratelimit.AdminDecision) map[string]any {
	source, _ := ratelimit.AdminSource(ip)
	return map[string]any{
		"reason": decision.Reason, "source": source, "retryAfter": decision.RetryAfter,
		"failureCount": decision.Failures, "banLevel": decision.BanLevel, "banStarted": decision.BanStarted,
	}
}

func (s *Server) beginAdminLogin(w http.ResponseWriter, r *http.Request) (adminLoginProtection, bool) {
	guard, err := s.adminLoginProtector()
	if err != nil {
		adminLoginUnavailable(w, r)
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	decision, err := guard.AdminAdmit(ctx, clientIP(r))
	if err != nil {
		adminLoginUnavailable(w, r)
		return nil, false
	}
	if !decision.Allowed {
		setOperationOutcome(r, "admin.login", "denied", adminLoginDetails(clientIP(r), decision))
		w.Header().Set("Retry-After", strconv.Itoa(decision.RetryAfter))
		fail(w, http.StatusTooManyRequests, "登录尝试过多，请稍后重试")
		return nil, false
	}
	return guard, true
}

func recordAdminLoginFailure(w http.ResponseWriter, r *http.Request, guard adminLoginProtection) bool {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	decision, err := guard.AdminFailure(ctx, clientIP(r))
	if err != nil {
		adminLoginUnavailable(w, r)
		return false
	}
	details := adminLoginDetails(clientIP(r), decision)
	if decision.Reason == "" {
		details["reason"] = "invalid_credentials"
	}
	setOperationOutcome(r, "admin.login", "denied", details)
	return true
}

func recordAdminLoginSuccess(w http.ResponseWriter, r *http.Request, guard adminLoginProtection) bool {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := guard.AdminSuccess(ctx, clientIP(r)); err != nil {
		adminLoginUnavailable(w, r)
		return false
	}
	setOperationOutcome(r, "admin.login", "success", map[string]any{"reason": "authenticated"})
	return true
}

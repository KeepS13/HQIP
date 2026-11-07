package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type failedLimiter struct{}

func (failedLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, errors.New("unavailable")
}

func TestSharedLimiterFailureCannotFallBackToProcessMemory(t *testing.T) {
	s, _ := newServer(t)
	s.opts.LoginLimiter = failedLimiter{}
	for _, h := range []http.HandlerFunc{s.login, s.nativeBegin, s.nativeToken} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"username":"admin","password":"initial-password-123"}`))
		r.RemoteAddr = "198.51.100.2:1234"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != 503 || w.Header().Get("Retry-After") != "5" {
			t.Fatalf("status %d", w.Code)
		}
	}
	if len(s.attempts) != 0 {
		t.Fatal("Redis failure used local counters")
	}
}

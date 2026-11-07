package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordMutationsShareLoginWorkBudget(t *testing.T) {
	s, _ := newServer(t)
	s.authSlots <- struct{}{}
	s.authSlots <- struct{}{}
	for _, handler := range []http.HandlerFunc{s.password, s.portalAdminPassword, s.clientAccount, func(w http.ResponseWriter, r *http.Request) { s.portalPassword(w, r, "test-user", "session") }} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"currentPassword":"guess","newPassword":"replacement-password"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
			t.Fatal("password hashing bypassed budget", w.Code)
		}
	}
	<-s.authSlots
	<-s.authSlots
}

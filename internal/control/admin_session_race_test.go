package control

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdminLoginRechecksPasswordAfterConcurrentReset(t *testing.T) {
	s, h := newServer(t)
	const oldPassword = "old-password-for-race-test"
	if err := s.Store.ChangePassword(oldPassword); err != nil {
		t.Fatal(err)
	}
	now := s.Store.Now()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	s.Store.Now = func() time.Time {
		// allowLogin reads time first; session creation reads it after hashing.
		if calls.Add(1) == 2 {
			close(entered)
			<-release
		}
		return now
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- request(h, "POST", "/api/v1/login", map[string]string{"username": "admin", "password": oldPassword}, "", "")
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("login did not reach the session barrier")
	}
	if err := s.Store.ChangePassword("new-password-for-race-test"); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case w := <-response:
		if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
			t.Fatalf("stale password created a session: status=%d cookies=%d", w.Code, len(w.Result().Cookies()))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("login remained blocked")
	}
	w := request(h, "POST", "/api/v1/login", map[string]string{"username": "admin", "password": "new-password-for-race-test"}, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("new password login failed: %d", w.Code)
	}
}

package control

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestOverloadRejectsBeforeExecutingHandlerAndRecovers(t *testing.T) {
	s, _ := newServer(t)
	entered := make(chan struct{}, 64)
	release := make(chan struct{})
	h := s.headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		entered <- struct{}{}
		<-release
		w.WriteHeader(204)
	}), s.opts.PublicURL)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/test", nil))
		}()
	}
	for i := 0; i < 64; i++ {
		<-entered
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/test", nil))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("overload was queued", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("liveness blocked")
	}
	close(release)
	wg.Wait()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/test", nil))
	if w.Code != 204 {
		t.Fatal("slot leaked")
	}
}

func TestReadinessRequiresDatabaseAndSharedLimiter(t *testing.T) {
	s, _ := newServer(t)
	h := s.PublicHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	s.opts.LoginLimiter = failedLimiter{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("Redis dependency ignored", w.Code)
	}
	s.opts.LoginLimiter = nil
	s.Store.Close()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("database dependency ignored", w.Code)
	}
}

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSlowAccountRequestCannotBlockLocalDisconnect(t *testing.T) {
	c, engine, _ := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/client/connections" {
			once.Do(func() { close(arrived) })
			<-release
		}
		_, _ = w.Write([]byte(`{"ok":true,"connections":[]}`))
	}))
	defer service.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	a, _ := NewAPI(service.URL, Tokens{AccessToken: "test", RefreshToken: "refresh", AccessExpiresAt: time.Now().Add(time.Hour)})
	c.mu.Lock()
	c.api = a
	c.mu.Unlock()
	requestDone := make(chan struct{})
	go func() { _, _ = a.Connections(context.Background()); close(requestDone) }()
	<-arrived
	persisted := make(chan error, 1)
	go func() { c.mu.Lock(); err := c.persistLocked(); c.mu.Unlock(); persisted <- err }()
	select {
	case err := <-persisted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("persist held controller lock waiting for network request")
	}
	disconnected := make(chan error, 1)
	go func() { disconnected <- c.Disconnect() }()
	deadline := time.Now().Add(time.Second)
	for engine.Alive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if engine.Alive() {
		t.Fatal("local engine stop waited for remote HTTP response")
	}
	releaseOnce.Do(func() { close(release) })
	<-requestDone
	if err := <-disconnected; err != nil {
		t.Fatal(err)
	}
}

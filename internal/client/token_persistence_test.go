package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRefreshPersistsBeforeProfileFailureAndSurvivesClientRestart(t *testing.T) {
	c, _, _ := setupClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/client/refresh" {
			json.NewEncoder(w).Encode(Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", SessionID: "session", AccessExpiresAt: time.Now().Add(time.Hour)})
			return
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	api, _ := NewAPI(srv.URL, Tokens{AccessToken: "old-access", RefreshToken: "old-refresh", SessionID: "session", AccessExpiresAt: time.Now().Add(-time.Minute)})
	c.api = api
	c.saved.ServerURL = srv.URL
	c.bindTokenPersistence(api)
	if err := c.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(context.Background()); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unexpected profile result: %v", err)
	}
	if !c.View().LoggedIn {
		t.Fatal("temporary profile failure signed out")
	}
	c.Close()
	reopened, err := New(c.dir, fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.View().LoggedIn || reopened.api.Tokens().RefreshToken != "new-refresh" {
		t.Fatal("restart used pre-rotation credentials")
	}
}
func TestLostRefreshResponseRetainsRequestIDAcrossRestart(t *testing.T) {
	c, _, _ := setupClient(t)
	var mu sync.Mutex
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/client/refresh" {
			w.WriteHeader(503)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		ids = append(ids, body["requestId"])
		first := len(ids) == 1
		mu.Unlock()
		if first {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", SessionID: "session", AccessExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer srv.Close()
	api, _ := NewAPI(srv.URL, Tokens{RefreshToken: "old-refresh", SessionID: "session"})
	c.api = api
	c.saved.ServerURL = srv.URL
	c.bindTokenPersistence(api)
	if _, _, _, err := api.Profile(context.Background(), ""); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("dropped response: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(c.dir, "session.dat"))
	if err != nil {
		t.Fatal(err)
	}
	var saved persisted
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Tokens.RefreshRequestID == "" || saved.Tokens.RefreshToken != "old-refresh" {
		t.Fatal("pending refresh not durable")
	}
	c.Close()
	reopened, err := New(c.dir, fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, _, _, _ = reopened.api.Profile(context.Background(), "")
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("restart changed rotation ID: %v", ids)
	}
	if reopened.api.Tokens().RefreshToken != "new-refresh" {
		t.Fatal("retry lost fresh tokens")
	}
}

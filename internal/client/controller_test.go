package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

type fakePlatform struct{}

func (fakePlatform) SecureDir(p string) error            { return os.MkdirAll(p, 0700) }
func (fakePlatform) Protect(b []byte) ([]byte, error)    { return b, nil }
func (fakePlatform) Unprotect(b []byte) ([]byte, error)  { return b, nil }
func (fakePlatform) Prepare(*exec.Cmd)                   {}
func (fakePlatform) Attach(*exec.Cmd) (io.Closer, error) { return nil, nil }
func (fakePlatform) EnableProxy(int) error               { return nil }
func (fakePlatform) RestoreProxy() error                 { return nil }
func (fakePlatform) StartTUN(context.Context, []byte) (func() error, error) {
	return nil, errors.New("not implemented")
}

type fakeEngine struct {
	mu      sync.Mutex
	fail    bool
	calls   []string
	alive   bool
	blocked chan struct{}
}

func (f *fakeEngine) Apply(ctx context.Context, p Profile, b rules.Bundle, n, m string, tun bool, s string) error {
	f.mu.Lock()
	f.calls = append(f.calls, n)
	fail := f.fail
	blocked := f.blocked
	f.mu.Unlock()
	if blocked != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-blocked:
		}
	}
	if fail {
		return errors.New("B unavailable")
	}
	f.mu.Lock()
	f.alive = true
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) Stop() error { f.mu.Lock(); defer f.mu.Unlock(); f.alive = false; return nil }
func (f *fakeEngine) Alive() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.alive }
func waitState(t *testing.T, c *Controller, want string) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		if c.View().RunState == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("want %s got %+v", want, c.View())
}
func setupClient(t *testing.T) (*Controller, *fakeEngine, func(Profile)) {
	t.Helper()
	var mu sync.Mutex
	p := Profile{SubscriptionID: "sub-a", SelectionRevision: 1, Subscription: &Subscription{ID: "sub-a", Status: "active", ExpiresAt: time.Now().Add(time.Hour), MaxConnections: 2}, DeviceID: "device", Name: "computer", Enabled: true, Revision: 1, DefaultNodeID: "A", Nodes: []Node{{ID: "A", Name: "A", Ready: true}, {ID: "B", Name: "B", Ready: true}}}
	p.Subscriptions = []Subscription{*p.Subscription}
	bundle := rules.Default()
	data, _ := rules.Canonical(bundle)
	hash, _ := rules.Hash(bundle)
	p.Rules = RulesRef{Version: 1, Hash: hash, URL: "/api/v3/client/rules/" + hash}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v3/client/profile":
			w.Header().Set("ETag", time.Now().String())
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v3/client/rules/" + hash:
			w.Write(data)
		case "/api/v3/client/connections":
			_ = json.NewEncoder(w).Encode(Connections{SubscriptionID: p.SubscriptionID, SelectionRevision: p.SelectionRevision, Connections: []Connection{}})
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	e := &fakeEngine{}
	c, err := New(t.TempDir(), fakePlatform{}, e)
	if err != nil {
		t.Fatal(err)
	}
	api, _ := NewAPI(srv.URL, Tokens{AccessToken: "test", RefreshToken: "test", AccessExpiresAt: time.Now().Add(time.Hour)})
	c.api = api
	c.saved.ServerURL = srv.URL
	c.view.LoggedIn = true
	if err = c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, e, func(next Profile) {
		mu.Lock()
		defer mu.Unlock()
		next.Subscription = p.Subscription
		next.SubscriptionID = p.SubscriptionID
		next.Subscriptions = p.Subscriptions
		next.SelectionRevision = p.SelectionRevision
		next.Rules = p.Rules
		next.DeviceID = p.DeviceID
		next.Enabled = true
		next.Name = p.Name
		p = next
	}
}
func TestOffUpdateNeverConnects(t *testing.T) {
	c, e, set := setupClient(t)
	set(Profile{Revision: 2, DefaultNodeID: "B", Nodes: []Node{{ID: "B", Ready: true}}})
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	v := c.View()
	if v.ConnectionIntent || v.DesiredNodeID != "B" || v.AppliedConfigRevision != 2 {
		t.Fatalf("bad cached state %+v", v)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.calls) != 0 {
		t.Fatal("off update started core")
	}
}
func TestFailedDefaultStopsWithoutFallback(t *testing.T) {
	c, e, set := setupClient(t)
	_ = c.Connect()
	waitState(t, c, "connected")
	e.mu.Lock()
	e.fail = true
	e.mu.Unlock()
	set(Profile{Revision: 2, DefaultNodeID: "B", Nodes: []Node{{ID: "B", Ready: true}}})
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "failed")
	v := c.View()
	if v.ConnectionIntent || v.ActualNodeID != "" || v.DesiredNodeID != "A" || v.LastError == "" {
		t.Fatalf("bad failed state %+v", v)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.alive || len(e.calls) != 1 || e.calls[0] != "A" {
		t.Fatalf("rollback or unexpected retry: %+v", e.calls)
	}
}
func TestUserDisconnectWinsPendingApply(t *testing.T) {
	c, e, _ := setupClient(t)
	e.blocked = make(chan struct{})
	_ = c.Connect()
	waitState(t, c, "connecting")
	_ = c.Disconnect()
	waitState(t, c, "disconnected")
	if c.View().ConnectionIntent || e.Alive() {
		t.Fatal("disconnect lost")
	}
}

func TestUserDisconnectWinsConfigUpdateApply(t *testing.T) {
	c, e, set := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	e.mu.Lock()
	e.blocked = make(chan struct{})
	e.mu.Unlock()
	set(Profile{Revision: 2, DefaultNodeID: "A", Nodes: []Node{{ID: "A", Ready: true, Password: "rotated"}, {ID: "B", Ready: true}}})
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connecting")
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "disconnected")
	if c.View().ConnectionIntent || e.Alive() {
		t.Fatal("config update reconnected after user disconnect")
	}
}

func TestStaleSyncResponsesCannotMutateReplacementSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized},
		{name: "disabled profile", statusCode: http.StatusOK},
		{name: "stale config", statusCode: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := setupClient(t)
			started := make(chan struct{})
			release := make(chan struct{})
			oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v3/client/refresh" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.URL.Path != "/api/v3/client/profile" {
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				close(started)
				<-release
				if tc.statusCode != http.StatusOK {
					w.WriteHeader(tc.statusCode)
					return
				}
				p := c.saved.Profile
				if tc.name == "disabled profile" {
					p.Enabled = false
				} else {
					p.Revision = 99
					p.DefaultNodeID = "B"
				}
				_ = json.NewEncoder(w).Encode(p)
			}))
			t.Cleanup(oldServer.Close)
			oldAPI, err := NewAPI(oldServer.URL, Tokens{AccessToken: "old", RefreshToken: "old", AccessExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			newAPI, err := NewAPI("http://127.0.0.1:1", Tokens{AccessToken: "new", RefreshToken: "new", AccessExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.api = oldAPI
			c.view.SyncError = "replacement session state"
			c.mu.Unlock()

			done := make(chan error, 1)
			go func() { done <- c.Sync(context.Background()) }()
			<-started
			c.mu.Lock()
			c.api = newAPI
			c.saved.Tokens = newAPI.Tokens()
			c.view.LoggedIn = true
			c.view.SyncError = "replacement session state"
			if tc.name == "stale config" {
				c.saved.Profile.Revision = 1
				c.view.DesiredNodeID = "A"
			}
			c.mu.Unlock()
			close(release)
			if err := <-done; tc.name == "unauthorized" && !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("want unauthorized error, got %v", err)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.api != newAPI || !c.view.LoggedIn || c.view.SyncError != "replacement session state" {
				t.Fatalf("stale response changed replacement session: api=%p view=%+v", c.api, c.view)
			}
			if tc.name == "stale config" && (c.saved.Profile.Revision != 1 || c.view.DesiredNodeID != "A") {
				t.Fatalf("stale profile committed: profile=%+v view=%+v", c.saved.Profile, c.view)
			}
		})
	}
}

func TestManualSelectionDuringProfileRefreshWinsDefaultChange(t *testing.T) {
	c, _, _ := setupClient(t)
	started := make(chan struct{})
	release := make(chan struct{})
	p := c.saved.Profile
	p.Revision = 2
	p.DefaultNodeID = "B"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/client/profile" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(p)
	}))
	t.Cleanup(srv.Close)
	api, err := NewAPI(srv.URL, Tokens{AccessToken: "test", RefreshToken: "test", AccessExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.api = api
	c.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- c.Refresh() }()
	<-started
	// Selection and credential persistence must finish while HTTP is still
	// blocked; observing a contended mutex depended on the old network-lock bug.
	if err := c.Select("B"); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := c.View().DesiredNodeID; got != "B" {
		t.Fatalf("in-flight default change overwrote manual selection: %s", got)
	}
}

func TestConcurrentReportsUseCurrentStatus(t *testing.T) {
	c, _, _ := setupClient(t)
	var mu sync.Mutex
	var statuses []Status
	firstArrived := make(chan struct{})
	releaseFirst := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/client/status" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var status Status
		if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
			t.Errorf("decode status: %v", err)
		}
		mu.Lock()
		statuses = append(statuses, status)
		first := len(statuses) == 1
		mu.Unlock()
		if first {
			close(firstArrived)
			<-releaseFirst
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	api, err := NewAPI(srv.URL, Tokens{AccessToken: "test", RefreshToken: "test", AccessExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.api = api
	c.view.RunState = "connected"
	c.view.ConnectionIntent = true
	c.mu.Unlock()
	firstDone := make(chan struct{})
	go func() { c.report(); close(firstDone) }()
	<-firstArrived
	c.mu.Lock()
	c.view.RunState = "disconnected"
	c.view.ConnectionIntent = false
	c.mu.Unlock()
	secondDone := make(chan struct{})
	go func() { c.report(); close(secondDone) }()
	close(releaseFirst)
	<-firstDone
	<-secondDone
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) != 2 {
		t.Fatalf("expected two status reports, got %d", len(statuses))
	}
	if statuses[0].RunState != "connected" || !statuses[0].ConnectionIntent {
		t.Fatalf("first report lost its status snapshot: %+v", statuses[0])
	}
	if statuses[1].RunState != "disconnected" || statuses[1].ConnectionIntent {
		t.Fatalf("newest report did not commit last: %+v", statuses[1])
	}
}

func TestAdminDefaultOverridesManualChoice(t *testing.T) {
	c, _, set := setupClient(t)
	_ = c.Select("B")
	set(Profile{Revision: 2, DefaultNodeID: "A", Nodes: []Node{{ID: "A", Ready: true}, {ID: "B", Ready: true}}})
	_ = c.Refresh()
	if c.View().DesiredNodeID != "B" {
		t.Fatal("unmodified default must preserve manual selection")
	}
	set(Profile{Revision: 3, DefaultNodeID: "B", Nodes: []Node{{ID: "A", Ready: true}, {ID: "B", Ready: true}}})
	_ = c.Refresh()
	_ = c.Select("A")
	set(Profile{Revision: 4, DefaultNodeID: "A", Nodes: []Node{{ID: "A", Ready: true}, {ID: "B", Ready: true}}})
	_ = c.Refresh()
	if c.View().DesiredNodeID != "A" {
		t.Fatal("administrator default did not override")
	}
}
func TestManagementClientDisablesProxyAndRedirect(t *testing.T) {
	a, e := NewAPI("https://example.com", Tokens{})
	if e != nil {
		t.Fatal(e)
	}
	if a.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("management traffic inherits proxy")
	}
	if _, e = NewAPI("http://example.com", Tokens{}); e == nil {
		t.Fatal("accepted insecure remote")
	}
	if a.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirect may leak bearer")
	}
}

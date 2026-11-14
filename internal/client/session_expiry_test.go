package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitLoggedOut(t *testing.T, c *Controller) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for c.View().LoggedIn && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.View().LoggedIn {
		t.Fatal("confirmed expired session did not leave the logged-in screen")
	}
}

func assertExpiredSessionCleared(t *testing.T, c *Controller, engine *fakeEngine) {
	t.Helper()
	waitLoggedOut(t, c)
	v := c.View()
	if engine.Alive() || v.ConnectionIntent || v.RunState != "disconnected" || v.DeviceName != "" ||
		v.Subscription != nil || len(v.Subscriptions) != 0 || len(v.Nodes) != 0 || len(v.NodeChoices) != 0 ||
		v.SelectionPending || v.NodeTests.Pending || v.LoginPending || v.ActualNodeID != "" || v.DesiredNodeID != "" {
		t.Fatalf("expired account retained connection or private data: %+v", v)
	}
	if v.LastError != ErrUnauthorized.Error() {
		t.Fatalf("missing login-expired reason: %q", v.LastError)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api != nil || c.saved.Tokens != (Tokens{}) || c.saved.Profile.DeviceID != "" ||
		c.saved.SelectedSubscriptionID != "" || c.saved.SelectedNodeID != "" ||
		c.nodeTest != nil || c.recovery != nil || c.recoveryReady || c.restoreSelection || c.suspended {
		t.Fatal("expired account retained internal authorization state")
	}
	data, err := os.ReadFile(filepath.Join(c.dir, "session.dat"))
	if err != nil {
		t.Fatal(err)
	}
	var saved persisted
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Tokens != (Tokens{}) || saved.Profile.DeviceID != "" || len(saved.Profile.Nodes) != 0 {
		t.Fatal("expired credentials survived in persisted session")
	}
	if _, err := os.Stat(filepath.Join(c.dir, "rules.json")); !os.IsNotExist(err) {
		t.Fatalf("expired account rules were retained: %v", err)
	}
}

func TestSessionExpiryClearsEveryAuthenticatedOperation(t *testing.T) {
	for _, operation := range []string{"profile", "rules", "connections", "report", "connect", "select", "recovery", "disabled-profile"} {
		t.Run(operation, func(t *testing.T) {
			c, engine, _ := setupClient(t)
			c.mu.Lock()
			profile := c.saved.Profile
			c.mu.Unlock()
			var refreshCalls, logoutCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/api/v3/client/")
				if path == "refresh" {
					refreshCalls.Add(1)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if path == "logout" {
					logoutCalls.Add(1)
				}
				reject := (operation == "profile" && path == "profile") ||
					(operation == "rules" && strings.HasPrefix(path, "rules/")) ||
					((operation == "connections" || operation == "recovery") && path == "connections") ||
					(operation == "report" && path == "status") ||
					(operation == "connect" && path == "connections/drain") ||
					(operation == "select" && path == "subscription")
				if reject {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if path == "profile" {
					p := profile
					if operation == "disabled-profile" {
						p.Enabled = false
					}
					_ = json.NewEncoder(w).Encode(p)
				} else if path == "connections" {
					_ = json.NewEncoder(w).Encode(Connections{SubscriptionID: profile.SubscriptionID, SelectionRevision: profile.SelectionRevision})
				} else {
					_, _ = w.Write([]byte(`{"ok":true}`))
				}
			}))
			defer server.Close()
			api, err := NewAPI(server.URL, Tokens{AccessToken: "expired", RefreshToken: "expired", SessionID: "expired", AccessExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			probeCtx, cancelProbe := context.WithCancel(context.Background())
			defer cancelProbe()
			c.mu.Lock()
			c.api = api
			c.saved.ServerURL, c.view.ServerURL = server.URL, server.URL
			c.view.ConnectionIntent, c.view.RunState = true, "connected"
			c.view.ActualNodeID, c.view.ActualSubscriptionID = "A", profile.SubscriptionID
			c.nodeTest = &nodeTestRun{api: api, cancel: cancelProbe}
			c.view.NodeTests.Pending = true
			if operation == "rules" {
				c.bundle.Rules = nil
			}
			if err := c.persistLocked(); err != nil {
				t.Fatal(err)
			}
			c.mu.Unlock()
			engine.mu.Lock()
			engine.alive = true
			engine.mu.Unlock()
			switch operation {
			case "profile", "rules", "disabled-profile":
				if err := c.Sync(context.Background()); !errors.Is(err, ErrUnauthorized) {
					t.Fatalf("expected session expiry, got %v", err)
				}
			case "connections":
				c.checkAccountConnection(context.Background())
			case "report":
				// report is also called by route operations while routeMu is held.
				done := make(chan struct{})
				go func() { c.routeMu.Lock(); c.report(); c.routeMu.Unlock(); close(done) }()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("status-report expiry deadlocked a route operation")
				}
			case "connect":
				c.mu.Lock()
				c.view.RunState = "cached"
				c.mu.Unlock()
				c.applyLatest()
			case "select":
				ctx, cancel := context.WithCancel(context.Background())
				c.mu.Lock()
				c.view.SelectionPending = true
				c.cancelSelection = cancel
				selection := c.selectionGeneration
				c.mu.Unlock()
				c.changeSubscription(ctx, cancel, api, profile.SubscriptionID, "", selection)
			case "recovery":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c.mu.Lock()
				c.recoveryReady = true
				r := &recoveryRun{api: api, generation: c.generation, selection: c.selectionGeneration, subscription: profile.SubscriptionID, node: "A", ctx: ctx, cancel: cancel}
				c.recovery = r
				c.mu.Unlock()
				_, _ = c.recoveryAttempt(r)
			}
			assertExpiredSessionCleared(t, c, engine)
			if probeCtx.Err() == nil {
				t.Fatal("node probe was not cancelled on session expiry")
			}
			if logoutCalls.Load() != 0 || refreshCalls.Load() > 1 {
				t.Fatalf("expiry performed redundant remote logout/refresh: %d/%d", logoutCalls.Load(), refreshCalls.Load())
			}
			restarted, err := New(c.dir, fakePlatform{}, &fakeEngine{})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if restarted.View().LoggedIn {
				t.Fatal("restart restored the expired account")
			}
		})
	}
}

func TestSessionExpiryOnlyAfterRefreshIsRejected(t *testing.T) {
	for _, mode := range []string{"refresh-success", "rate-limit", "server-error", "deadline", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			c, _, _ := setupClient(t)
			profile := c.saved.Profile
			var refreshCalls atomic.Int32
			releaseDeadline := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/refresh") {
					refreshCalls.Add(1)
					switch mode {
					case "rate-limit":
						w.WriteHeader(429)
					case "server-error":
						w.WriteHeader(503)
					case "deadline":
						<-releaseDeadline
					default:
						_ = json.NewEncoder(w).Encode(Tokens{AccessToken: "fresh", RefreshToken: "fresh-refresh", SessionID: "same-session", AccessExpiresAt: time.Now().Add(time.Hour)})
					}
					return
				}
				if mode == "forbidden" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":"subscription unavailable"}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer fresh" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(profile)
			}))
			defer server.Close()
			defer close(releaseDeadline)
			api, _ := NewAPI(server.URL, Tokens{AccessToken: "old-access", RefreshToken: "valid-refresh", SessionID: "same-session", AccessExpiresAt: time.Now().Add(time.Hour)})
			c.mu.Lock()
			c.api = api
			c.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := c.Sync(ctx)
			if !c.View().LoggedIn || c.api != api {
				t.Fatalf("non-expired session logged out after %s: %v", mode, err)
			}
			if mode == "refresh-success" {
				if err != nil || api.Tokens().AccessToken != "fresh" || refreshCalls.Load() != 1 {
					t.Fatalf("access token refresh failed: %v", err)
				}
			} else if err == nil || errors.Is(err, ErrUnauthorized) {
				t.Fatalf("non-auth failure was misclassified: %v", err)
			}
			if mode == "forbidden" && refreshCalls.Load() != 0 {
				t.Fatal("subscription permission failure rotated account credentials")
			}
		})
	}
}

func TestExpiredStatusResponseCannotLogOutReplacementAccount(t *testing.T) {
	c, _, _ := setupClient(t)
	oldAPI := c.api
	newAPI, _ := NewAPI("http://127.0.0.1:1", Tokens{AccessToken: "new", RefreshToken: "new", SessionID: "new", AccessExpiresAt: time.Now().Add(time.Hour)})
	c.mu.Lock()
	c.api = newAPI
	c.saved.Tokens = newAPI.Tokens()
	c.mu.Unlock()
	if err := c.expireSession(oldAPI); err != nil {
		t.Fatal(err)
	}
	if !c.View().LoggedIn || c.api != newAPI || c.saved.Tokens.SessionID != "new" {
		t.Fatal("stale expiry cleared the replacement account")
	}
}

type failingSessionProtection struct{ fakePlatform }

func (failingSessionProtection) Protect([]byte) ([]byte, error) {
	return nil, errors.New("simulated credential protection failure")
}

func TestSessionExpiryRemovesOldCredentialsWhenPersistenceFails(t *testing.T) {
	c, _, _ := setupClient(t)
	api := c.api
	if _, err := os.Stat(filepath.Join(c.dir, "session.dat")); err != nil {
		t.Fatal("fixture did not persist a logged-in session", err)
	}
	c.platform = failingSessionProtection{}
	if err := c.expireSession(api); err == nil {
		t.Fatal("credential persistence failure was suppressed")
	}
	if c.View().LoggedIn || c.api != nil || c.saved.Tokens != (Tokens{}) {
		t.Fatal("persistence failure left the expired session in memory")
	}
	if _, err := os.Stat(filepath.Join(c.dir, "session.dat")); !os.IsNotExist(err) {
		t.Fatalf("old encrypted session survived failed logged-out write: %v", err)
	}
	restarted, err := New(c.dir, fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.View().LoggedIn {
		t.Fatal("restarting restored expired credentials after a failed write")
	}
}

package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserAuthorizationBindsStatePKCEAndLoopback(t *testing.T) {
	var request map[string]string
	var service *httptest.Server
	service = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/client/authorize":
			_ = json.NewDecoder(r.Body).Decode(&request)
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationUrl": service.URL + "/authorize/request"})
		case "/api/v3/client/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			digest := sha256.Sum256([]byte(body["codeVerifier"]))
			if base64.RawURLEncoding.EncodeToString(digest[:]) != request["codeChallenge"] || body["redirectUri"] != request["redirectUri"] || body["installationId"] != "installation" {
				t.Error("PKCE binding lost")
			}
			_ = json.NewEncoder(w).Encode(Tokens{AccessToken: "access", RefreshToken: "refresh", SessionID: "session"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer service.Close()
	api, _ := NewAPI(service.URL, Tokens{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := api.BrowserLogin(ctx, "installation", "test", func(address string) error {
		if address != service.URL+"/authorize/request" {
			t.Fatal("untrusted navigation")
		}
		for _, state := range []string{"wrong", request["state"]} {
			response, e := http.Get(request["redirectUri"] + "?code=" + strings.Repeat("x", 40) + "&state=" + url.QueryEscape(state))
			if e != nil {
				return e
			}
			response.Body.Close()
			if state == "wrong" && response.StatusCode != 400 {
				t.Error("invalid state accepted")
			}
			if state != "wrong" && response.StatusCode != 200 {
				t.Error("valid callback rejected")
			}
		}
		return nil
	})
	if err != nil || api.Tokens().SessionID != "session" {
		t.Fatalf("login failed: %v", err)
	}
}

func TestUntrustedAuthorizationURLIsNeverOpened(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"authorizationUrl": "https://untrusted.invalid/"})
	}))
	defer service.Close()
	api, _ := NewAPI(service.URL, Tokens{})
	if err := api.BrowserLogin(context.Background(), "installation", "test", func(string) error { t.Fatal("opened foreign URL"); return nil }); err == nil {
		t.Fatal("accepted foreign origin")
	}
}

func TestDefaultChangePreservesAllowedChoice(t *testing.T) {
	c, _, set := setupClient(t)
	set(Profile{Revision: 2, DefaultNodeID: "B", Nodes: []Node{{ID: "A", Ready: true}, {ID: "B", Ready: true}}})
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if c.View().DesiredNodeID != "A" {
		t.Fatal("default change displaced allowed selection")
	}
	set(Profile{Revision: 1, DefaultNodeID: "B", Nodes: []Node{{ID: "B", Ready: true}}})
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if c.View().Revision != 2 || c.View().DesiredNodeID != "A" {
		t.Fatal("older response replaced newer configuration")
	}
}

func TestNoSubscriptionKeepsAccountLoggedInButStopsProxy(t *testing.T) {
	c, engine, _ := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	p := c.saved.Profile
	p.Revision++
	p.Subscription = nil
	p.Nodes = nil
	if err := c.cacheUnavailable(c.api, p); err != nil {
		t.Fatal(err)
	}
	v := c.View()
	if !v.LoggedIn || v.ConnectionIntent || v.ActualNodeID != "" || engine.Alive() {
		t.Fatal("subscription ended without stopping route or logged account out")
	}
	if err := c.Connect(); err == nil {
		t.Fatal("unsubscribed account connected")
	}
}

func TestWaitDrainedRequiresServerClosure(t *testing.T) {
	var calls int
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		state := "closing"
		if calls >= 2 {
			state = "closed"
		}
		_ = json.NewEncoder(w).Encode(Connections{SubscriptionID: "sub-a", SelectionRevision: 3, Connections: []Connection{{ID: "one", SubscriptionID: "sub-a", State: state}}})
	}))
	defer service.Close()
	api, _ := NewAPI(service.URL, Tokens{AccessToken: "access", RefreshToken: "refresh", AccessExpiresAt: time.Now().Add(time.Hour)})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := api.WaitDrained(ctx, "sub-a", 3); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatal("released unconfirmed connection")
	}
}

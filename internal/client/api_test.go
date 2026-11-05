package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRulesAcceptsSameOriginProfileURL(t *testing.T) {
	path := "/api/v3/client/rules/" + strings.Repeat("a", 64)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != path || r.Header.Get("Authorization") != "Bearer ACCESS" {
			t.Errorf("wrong rules request path or authorization")
		}
		_, _ = w.Write([]byte(`{"schemaVersion":1}`))
	}))
	defer server.Close()
	api, err := NewAPI(server.URL, Tokens{AccessToken: "ACCESS", RefreshToken: "REFRESH", AccessExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{server.URL + path, path} {
		got, err := api.Rules(context.Background(), address)
		if err != nil || string(got) != `{"schemaVersion":1}` {
			t.Fatalf("rules fetch failed: %q %v", got, err)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("rules were not fetched")
	}
}

func TestSelectSubscriptionCarriesCurrentSessionRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/client/subscription" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer ACCESS" {
			t.Errorf("wrong subscription request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["subscriptionId"] != "instance-b" || body["expectedRevision"] != float64(9) {
			t.Errorf("stale selection revision: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(Profile{SubscriptionID: "instance-b", SelectionRevision: 10, Subscription: &Subscription{ID: "instance-b", Status: "active"}, Subscriptions: []Subscription{{ID: "instance-a"}, {ID: "instance-b"}}})
	}))
	defer server.Close()
	a, _ := NewAPI(server.URL, Tokens{AccessToken: "ACCESS", RefreshToken: "REFRESH", AccessExpiresAt: time.Now().Add(time.Hour)})
	p, err := a.SelectSubscription(context.Background(), "instance-b", 9)
	if err != nil {
		t.Fatal(err)
	}
	if p.SubscriptionID != "instance-b" || p.Subscription == nil || p.Subscription.ID != "instance-b" || p.SelectionRevision != 10 || len(p.Subscriptions) != 2 {
		t.Fatalf("bad selection response: %+v", p)
	}
}

func TestRulesRejectsCrossOriginAndOtherResourcesWithoutSendingToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	api, err := NewAPI(server.URL, Tokens{AccessToken: "ACCESS", RefreshToken: "REFRESH", AccessExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v3/client/rules/" + strings.Repeat("a", 64)
	for _, address := range []string{
		"https://untrusted.invalid" + path,
		"//untrusted.invalid" + path,
		strings.Replace(server.URL, "http://", "https://", 1) + path,
		server.URL + "/api/v3/client/profile",
		server.URL + path + "?token=secret",
		server.URL + path + "#fragment",
		server.URL + path + "/../profile",
		server.URL + "/api/v3/client/rules/%61" + strings.Repeat("a", 63),
		server.URL + "/api/v3/client/rules/" + strings.Repeat("g", 64),
	} {
		if _, err := api.Rules(context.Background(), address); err == nil {
			t.Errorf("accepted invalid rules address %q", address)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid rules address sent a request")
	}
}

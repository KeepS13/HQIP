package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

type subscriptionEngine struct {
	fakeEngine
	subscriptions []string
	passwords     []string
}

func (e *subscriptionEngine) Apply(ctx context.Context, p Profile, bundle rules.Bundle, node, mode string, tun bool, origin string) error {
	e.mu.Lock()
	e.subscriptions = append(e.subscriptions, p.SubscriptionID)
	e.passwords = append(e.passwords, p.Nodes[0].Password)
	e.mu.Unlock()
	return e.fakeEngine.Apply(ctx, p, bundle, node, mode, tun, origin)
}

type subscriptionService struct {
	mu           sync.Mutex
	selected     string
	revision     int64
	drains       []string
	selectedHook func(string)
	failure      string
}

func subscriptionClient(t *testing.T) (*Controller, *subscriptionEngine, *subscriptionService) {
	t.Helper()
	s := &subscriptionService{selected: "a", revision: 1}
	bundle := rules.Default()
	data, _ := rules.Canonical(bundle)
	hash, _ := rules.Hash(bundle)
	instances := []Subscription{}
	for _, id := range []string{"a", "b", "c"} {
		instances = append(instances, Subscription{ID: id, TemplateName: "Same plan", Status: "active", ExpiresAt: time.Now().Add(time.Hour), MaxConnections: 1, DefaultNodeID: "same-node", NodeIDs: []string{"same-node"}})
	}
	profile := func() Profile { // caller holds service lock
		p := Profile{DeviceID: "user", Enabled: true, Revision: 1, SubscriptionID: s.selected, SelectionRevision: s.revision, Subscriptions: instances, Rules: RulesRef{Hash: hash, Version: 1, URL: "/api/v3/client/rules/" + hash}}
		for _, sub := range instances {
			if sub.ID == s.selected {
				p.Subscription = &sub
				p.DefaultNodeID = "same-node"
				p.Nodes = []Node{{ID: "same-node", Password: "credential-" + s.selected, Ready: true}}
			}
		}
		return p
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/api/v3/client/profile":
			_ = json.NewEncoder(w).Encode(profile())
		case "/api/v3/client/rules/" + hash:
			_, _ = w.Write(data)
		case "/api/v3/client/connections":
			_ = json.NewEncoder(w).Encode(Connections{SubscriptionID: s.selected, SelectionRevision: s.revision, Connections: []Connection{}})
		case "/api/v3/client/connections/drain":
			var input struct {
				SubscriptionID string `json:"subscriptionId"`
				Revision       int64  `json:"expectedRevision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.SubscriptionID != s.selected || input.Revision != s.revision {
				w.WriteHeader(409)
				return
			}
			s.drains = append(s.drains, input.SubscriptionID)
			_, _ = w.Write([]byte(`{}`))
		case "/api/v3/client/subscription":
			var input struct {
				SubscriptionID string `json:"subscriptionId"`
				Revision       int64  `json:"expectedRevision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Revision != s.revision {
				w.WriteHeader(409)
				return
			}
			if input.SubscriptionID == s.failure {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"subscription expired"}`))
				return
			}
			s.selected = input.SubscriptionID
			s.revision++
			p := profile()
			hook := s.selectedHook
			s.mu.Unlock()
			if hook != nil {
				hook(input.SubscriptionID)
			}
			s.mu.Lock()
			_ = json.NewEncoder(w).Encode(p)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	e := &subscriptionEngine{}
	c, err := New(t.TempDir(), fakePlatform{}, e)
	if err != nil {
		t.Fatal(err)
	}
	c.api, _ = NewAPI(srv.URL, Tokens{AccessToken: "access", RefreshToken: "refresh", AccessExpiresAt: time.Now().Add(time.Hour)})
	c.saved.ServerURL = srv.URL
	c.view.LoggedIn = true
	if err = c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, e, s
}
func waitSubscription(t *testing.T, c *Controller, id string) {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		v := c.View()
		if !v.SelectionPending && v.Subscription != nil && v.Subscription.ID == id {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscription did not settle on %s: %+v", id, c.View())
}

func TestOffSubscriptionSwitchDoesNotStartProxy(t *testing.T) {
	c, e, s := subscriptionClient(t)
	if err := c.SelectSubscription("b"); err != nil {
		t.Fatal(err)
	}
	waitSubscription(t, c, "b")
	if v := c.View(); v.ConnectionIntent || v.ActualSubscriptionID != "" || v.DesiredSubscriptionID != "b" {
		t.Fatalf("off switch connected: %+v", v)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.subscriptions) != 0 {
		t.Fatal("off switch started core")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.drains) != 0 || s.selected != "a" {
		t.Fatal("idle choice changed server authorization or drained the previous instance")
	}
}

func TestSameNodeSubscriptionSwitchReauthenticates(t *testing.T) {
	c, e, _ := subscriptionClient(t)
	_ = c.Connect()
	waitState(t, c, "connected")
	if err := c.SelectSubscription("b"); err != nil {
		t.Fatal(err)
	}
	waitSubscription(t, c, "b")
	waitState(t, c, "connected")
	if c.View().ActualSubscriptionID != "b" {
		t.Fatal("actual identity lost")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.subscriptions) != 2 || e.subscriptions[0] != "a" || e.subscriptions[1] != "b" || e.passwords[0] == e.passwords[1] {
		t.Fatalf("reused previous billing identity: %v %v", e.subscriptions, e.passwords)
	}
}

func TestRapidSubscriptionSelectionIgnoresLateCommittedResponse(t *testing.T) {
	c, e, s := subscriptionClient(t)
	_ = c.Connect()
	waitState(t, c, "connected")
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.mu.Lock()
	s.selectedHook = func(id string) {
		if id == "b" {
			once.Do(func() { close(arrived) })
			<-release
		}
	}
	s.mu.Unlock()
	if err := c.SelectSubscription("b"); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, arrived, "automatic switch did not start")
	if err := c.SelectSubscription("c"); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitSubscription(t, c, "c")
	waitState(t, c, "connected")
	time.Sleep(30 * time.Millisecond)
	if c.View().ActualSubscriptionID != "c" || c.View().DesiredSubscriptionID != "c" {
		t.Fatal("late B replaced C")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range e.subscriptions {
		if id == "b" {
			t.Fatal("superseded authorization started an intermediate connection")
		}
	}
}

func TestDisconnectDuringSubscriptionSwitchPreventsReconnect(t *testing.T) {
	c, e, s := subscriptionClient(t)
	_ = c.Connect()
	waitState(t, c, "connected")
	arrived, release := make(chan struct{}), make(chan struct{})
	s.mu.Lock()
	s.selectedHook = func(id string) {
		if id == "b" {
			close(arrived)
			<-release
		}
	}
	s.mu.Unlock()
	_ = c.SelectSubscription("b")
	<-arrived
	disconnected := make(chan error, 1)
	go func() { disconnected <- c.Disconnect() }()
	until := time.Now().Add(time.Second)
	for e.Alive() && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if e.Alive() {
		t.Fatal("disconnect waited on selection HTTP")
	}
	close(release)
	<-disconnected
	waitSubscription(t, c, "b")
	if c.View().ConnectionIntent || e.Alive() {
		t.Fatal("switch reconnected after user disconnect")
	}
}

func TestFailedSubscriptionSelectionNeverFallsBack(t *testing.T) {
	c, e, s := subscriptionClient(t)
	_ = c.Connect()
	waitState(t, c, "connected")
	s.mu.Lock()
	s.failure = "b"
	s.mu.Unlock()
	_ = c.SelectSubscription("b")
	waitState(t, c, "failed")
	if e.Alive() || c.View().ConnectionIntent || c.View().ActualSubscriptionID != "" {
		t.Fatal("failed switch retained old tunnel")
	}
	if c.View().LastError == "" {
		t.Fatal("failure hidden")
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "failed")
	if c.View().ActualSubscriptionID != "" || e.Alive() {
		t.Fatal("failed target fell back to cached subscription")
	}
	if err := c.SelectSubscription("a"); err != nil {
		t.Fatal(err)
	}
	waitSubscription(t, c, "a")
	if c.View().DesiredSubscriptionID != "a" || c.View().LastError != "" {
		t.Fatal("explicit previous-instance choice was ignored")
	}
}

func TestSelectionPreferencesAreAccountScopedAndDoNotConnect(t *testing.T) {
	c, e, _ := subscriptionClient(t)
	_ = c.SelectSubscription("b")
	waitSubscription(t, c, "b")
	c.mu.Lock()
	saved := c.saved.Preferences[c.preferenceKeyLocked(c.saved.Profile)]
	c.mu.Unlock()
	if saved.SubscriptionID != "b" || saved.NodeIDs["b"] != "same-node" {
		t.Fatal("explicit selection not remembered")
	}
	p := Profile{DeviceID: "other-user", Subscriptions: c.View().Subscriptions}
	c.mu.Lock()
	c.restoreSelection = true
	c.mu.Unlock()
	c.restoreRemembered(c.api, p)
	if c.View().SelectionPending || e.Alive() {
		t.Fatal("other account inherited selected subscription")
	}
}

func TestDrainRejectsChangedSelection(t *testing.T) {
	c, _, s := subscriptionClient(t)
	s.mu.Lock()
	s.selected = "b"
	s.revision = 2
	s.mu.Unlock()
	if err := c.api.Drain(context.Background(), "a", 1); err != ErrConflict {
		t.Fatalf("late drain did not conflict: %v", err)
	}
	if err := c.api.WaitDrained(context.Background(), "a", 1); err != ErrConflict {
		t.Fatalf("stale completion accepted: %v", err)
	}
}

func TestFirstLoginDoesNotChoosePackageAndRememberedChoiceStaysOff(t *testing.T) {
	c, e, s := subscriptionClient(t)
	s.mu.Lock()
	s.selected = ""
	s.revision++
	s.mu.Unlock()
	c.mu.Lock()
	c.saved.Preferences = map[string]SelectionPreference{}
	c.restoreSelection = true
	c.mu.Unlock()
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if v := c.View(); v.Subscription != nil || v.SelectionPending || v.DesiredSubscriptionID != "" {
		t.Fatal("first login silently chose a package")
	}
	_ = c.SelectSubscription("b")
	waitSubscription(t, c, "b")
	s.mu.Lock()
	s.selected = ""
	s.revision++
	s.mu.Unlock()
	c.mu.Lock()
	c.restoreSelection = true
	c.mu.Unlock()
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	waitSubscription(t, c, "b")
	if c.View().ConnectionIntent || e.Alive() {
		t.Fatal("restored preference also restored connection intent")
	}
}

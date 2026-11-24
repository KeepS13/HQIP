package client

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Selection must be a local transaction while disconnected, regardless of
// whether the current credentials support offline recovery. Count every HTTP
// call (including status reports), not just calls to the selection endpoint.
type selectionTransport struct {
	base       http.RoundTripper
	calls      atomic.Int64
	rules      atomic.Int64
	selections atomic.Int64
}

func (s *selectionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	if strings.Contains(r.URL.Path, "/rules/") {
		s.rules.Add(1)
	}
	if r.URL.Path == "/api/v3/client/subscription" {
		s.selections.Add(1)
	}
	return s.base.RoundTrip(r)
}

func TestIdleNodeChoiceDoesNoNetworkAndSurvivesRefresh(t *testing.T) {
	c, e, _ := subscriptionClient(t)
	transport := &selectionTransport{base: c.api.http.Transport}
	c.api.http.Transport = transport
	c.mu.Lock()
	c.saved.Profile.Nodes[0].OfflineAdmissionVersion = 1
	c.mu.Unlock()
	if err := c.SelectSubscriptionNode("b", "same-node"); err != nil {
		t.Fatal(err)
	}
	v := c.View()
	if v.SelectionPending || v.ConnectionIntent || v.RunState == "connecting" || v.DesiredSubscriptionID != "b" || v.DesiredNodeID != "same-node" {
		t.Fatalf("idle choice was not immediately published: %+v", v)
	}
	if transport.calls.Load() != 0 || e.Alive() {
		t.Fatal("idle choice contacted account service or started proxy")
	}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if v = c.View(); v.DesiredSubscriptionID != "b" || v.Subscription == nil || v.Subscription.ID != "b" {
		t.Fatal("background profile replaced local choice")
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	if v = c.View(); v.ActualSubscriptionID != "b" || v.ActualNodeID != "same-node" {
		t.Fatal("connect did not authorize the chosen target")
	}
	if transport.rules.Load() != 0 {
		t.Fatal("unchanged verified rules were downloaded again")
	}
}

type selectionEngine struct {
	*subscriptionEngine
	stops atomic.Int64
}

func (e *selectionEngine) Stop() error { e.stops.Add(1); return e.subscriptionEngine.Stop() }

func TestFirstChoiceAndIdleRefreshNeverTouchEngine(t *testing.T) {
	c, original, service := subscriptionClient(t)
	engine := &selectionEngine{subscriptionEngine: original}
	c.engine = engine
	c.api.http.Transport = pickerCatalogTransport{next: c.api.http.Transport}
	service.mu.Lock()
	service.selected = ""
	service.revision++
	service.mu.Unlock()
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := c.SelectSubscriptionNode("b", "alternate"); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if engine.stops.Load() != 0 || engine.Alive() {
		t.Fatal("idle catalog/choice changed the engine")
	}
	if v := c.View(); v.DesiredNodeID != "alternate" || v.DesiredSubscriptionID != "b" || v.SelectionPending {
		t.Fatal("first choice was overwritten")
	}
	if v := c.View(); len(v.Nodes) != 2 || v.Nodes[1].ID != "alternate" {
		t.Fatal("legacy tray shows the authorized package instead of the locally selected package")
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	if v := c.View(); v.ActualNodeID != "alternate" || v.ActualSubscriptionID != "b" {
		t.Fatal("first connection used a different target")
	}
}

func TestLocalChoicePersistsAndIsAccountScoped(t *testing.T) {
	c, _, _ := subscriptionClient(t)
	for _, id := range []string{"b", "c", "b"} {
		if err := c.SelectSubscriptionNode(id, "same-node"); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	c.Close()
	reopened, err := New(c.dir, fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	v := reopened.View()
	if v.DesiredSubscriptionID != "b" || v.DesiredNodeID != "same-node" || v.ConnectionIntent || v.SelectionPending {
		t.Fatal("reopened client lost choice or connected")
	}
	reopened.mu.Lock()
	pref := reopened.saved.Preferences[reopened.preferenceKeyLocked(reopened.saved.Profile)]
	reopened.saved.Profile.DeviceID = "different-account"
	reopened.mu.Unlock()
	if pref.SubscriptionID != "b" {
		t.Fatal("profile polling rewrote the remembered local choice")
	}
	if reopened.View().DesiredSubscriptionID == "b" {
		t.Fatal("local draft leaked to another account")
	}
}

type selectionFailPlatform struct{ fakePlatform }

func (selectionFailPlatform) Protect([]byte) ([]byte, error) {
	return nil, errors.New("fixture persistence failure")
}

func TestLocalChoiceWriteFailureDoesNotPublish(t *testing.T) {
	c, _, _ := subscriptionClient(t)
	if err := c.SelectSubscriptionNode("b", "same-node"); err != nil {
		t.Fatal(err)
	}
	c.platform = selectionFailPlatform{}
	if err := c.SelectSubscriptionNode("c", "same-node"); err == nil {
		t.Fatal("ignored persistence failure")
	}
	if v := c.View(); v.DesiredSubscriptionID != "b" || v.SelectionPending {
		t.Fatal("failed save published C")
	}
	if err := c.SelectSubscriptionNode("b", "foreign-node"); err == nil {
		t.Fatal("accepted unowned node")
	}
}

func TestLateProfileCannotReplaceIdleChoice(t *testing.T) {
	c, _, _ := subscriptionClient(t)
	base := c.api.http.Transport
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	unlock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unlock)
	c.api.http.Transport = accountTransportFunc(func(r *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(r)
		if r.URL.Path == "/api/v3/client/profile" {
			once.Do(func() { close(started); <-release })
		}
		return response, err
	})
	done := make(chan error, 1)
	go func() { done <- c.Refresh() }()
	awaitClosedSignal(t, started, "profile did not start")
	if err := c.SelectSubscriptionNode("b", "same-node"); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if v := c.View(); v.DesiredSubscriptionID != "b" || v.SelectionPending {
		t.Fatal("late authorized profile replaced local draft")
	}
}

func TestDisconnectedNewChoiceWinsLateRemoteSwitch(t *testing.T) {
	c, engine, service := subscriptionClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unlock)
	service.mu.Lock()
	service.selectedHook = func(id string) {
		if id == "b" {
			close(started)
			<-release
		}
	}
	service.mu.Unlock()
	if err := c.SelectSubscription("b"); err != nil {
		t.Fatal(err)
	}

	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, started, "switch did not start")
	start := time.Now()
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("disconnect waited for blocked account reporting")
	}
	if err := c.SelectSubscriptionNode("c", "same-node"); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if v := c.View(); v.DesiredSubscriptionID != "c" || v.SelectionPending || v.ConnectionIntent || engine.Alive() {
		t.Fatal("late remote switch changed disconnected choice")
	}
}

func TestConnectedSamePackageNodeChangeAvoidsSubscriptionWork(t *testing.T) {
	c, engine, _ := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	transport := &selectionTransport{base: c.api.http.Transport}
	c.api.http.Transport = transport
	if err := c.SelectSubscriptionNode("sub-a", "B"); err != nil {
		t.Fatal(err)
	}
	if c.View().ActualNodeID != "A" || transport.calls.Load() != 0 {
		t.Fatal("selection changed the active transport")
	}

	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for c.View().ActualNodeID != "B" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.View().ActualNodeID != "B" {
		t.Fatal("node did not change")
	}
	if transport.rules.Load() != 0 || transport.selections.Load() != 0 {
		t.Fatal("same-package node switch refreshed its subscription")
	}
	engine.mu.Lock()
	calls := len(engine.calls)
	engine.mu.Unlock()
	if err := c.SelectSubscriptionNode("sub-a", "B"); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.calls) != calls {
		t.Fatal("same node restarted the connection")
	}
}

func TestConnectedChoiceSaveFailureKeepsWorkingRoute(t *testing.T) {
	c, engine, _ := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	c.routeMu.Lock() // Wait for the successful apply/report to finish first.
	c.mu.Lock()
	generation := c.generation
	c.platform = selectionFailPlatform{}
	c.mu.Unlock()
	c.routeMu.Unlock()
	if err := c.SelectSubscriptionNode("sub-a", "B"); err == nil {
		t.Fatal("ignored failed preference save")
	}
	c.mu.Lock()
	unchanged := c.generation == generation && c.saved.SelectedNodeID == "A"
	c.mu.Unlock()
	if !unchanged || !engine.Alive() || c.View().DesiredNodeID != "A" {
		t.Fatal("failed preference save interrupted the working route")
	}
}

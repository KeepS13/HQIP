package client

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Block route work while accepting many choices: selection must return with
// immediate feedback, and only the final queued target may start a transport.
func TestActiveChoicesCoalesceWithoutCatalogReload(t *testing.T) {
	c, engine, _ := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	c.routeMu.Lock()
	var once sync.Once
	unlock := func() { once.Do(c.routeMu.Unlock) }
	defer unlock()
	transport := &selectionTransport{base: c.api.http.Transport}
	c.api.http.Transport = transport
	var probes atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { probes.Add(1); return 1, nil }
	engine.mu.Lock()
	calls := len(engine.calls)
	engine.mu.Unlock()
	for i := 0; i < 41; i++ {
		id := "B"
		if i%2 == 1 {
			id = "A"
		}
		if err := c.SelectSubscriptionNode("sub-a", id); err != nil {
			t.Fatal(err)
		}
		v := c.View()
		if v.DesiredNodeID != id || v.ActualNodeID != "A" || v.RunState != "connecting" || !v.SelectionPending {
			t.Fatal("queued choice was not immediately shown or relabeled the old transport", v.Status)
		}
	}
	if transport.calls.Load() != 0 {
		t.Fatal("selection waited on account I/O")
	}
	unlock()
	waitState(t, c, "connected")
	engine.mu.Lock()
	currentCalls := len(engine.calls)
	engine.mu.Unlock()
	if c.View().ActualNodeID != "B" || currentCalls != calls+1 || probes.Load() != 0 {
		t.Fatal("did not coalesce onto final target, or ran catalog probes", currentCalls, probes.Load())
	}
	if transport.rules.Load() != 0 || transport.selections.Load() != 0 {
		t.Fatal("same-subscription switch reimported rules or subscription credentials")
	}
}

func TestActiveCrossSubscriptionReauthorizesWithoutRuleReload(t *testing.T) {
	c, engine, _ := subscriptionClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	c.routeMu.Lock()
	transport := &selectionTransport{base: c.api.http.Transport}
	c.api.http.Transport = transport
	c.routeMu.Unlock()
	if err := c.SelectSubscriptionNode("b", "same-node"); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	if v := c.View(); v.ActualSubscriptionID != "b" || v.DesiredSubscriptionID != "b" {
		t.Fatal("active choice did not automatically connect the target", v.Status)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.passwords) != 2 || engine.passwords[0] == engine.passwords[1] || transport.rules.Load() != 0 || transport.selections.Load() != 1 {
		t.Fatal("switch reused the old identity or repeated catalog/rule imports")
	}
}

func TestDisconnectCancelsQueuedChoiceAndDoesNotReconnect(t *testing.T) {
	c, engine, _ := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	c.routeMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(c.routeMu.Unlock) }
	defer unlock()
	if err := c.Select("B"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Disconnect() }()
	// Wait for intent revocation before allowing either route worker through.
	deadline := time.Now().Add(time.Second)
	for c.View().ConnectionIntent && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.View().ConnectionIntent {
		t.Fatal("disconnect waited for route work before revoking intent")
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.Close() // Joins all queued workers before observing the final state.
	v := c.View()
	if v.ConnectionIntent || v.SelectionPending || v.ActualNodeID != "" || engine.Alive() || v.DesiredNodeID != "B" {
		t.Fatal("queued selection resurrected a disconnected transport", v.Status)
	}
}

func TestCanceledRemoteChoiceCanReturnToAuthorizedNode(t *testing.T) {
	c, _, service := subscriptionClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	defer unlock()
	service.mu.Lock()
	service.selectedHook = func(id string) {
		if id == "b" {
			close(entered)
			<-release
		}
	}
	service.mu.Unlock()
	if err := c.SelectSubscription("b"); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, entered, "B authorization did not commit")
	if err := c.SelectSubscription("a"); err != nil {
		t.Fatal(err)
	}
	unlock()
	waitState(t, c, "connected")
	if v := c.View(); v.ActualSubscriptionID != "a" || v.DesiredSubscriptionID != "a" {
		t.Fatal("canceled remote choice prevented returning to A", v.Status)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.selected != "a" {
		t.Fatal("reused stale cached authorization after committed B")
	}
}

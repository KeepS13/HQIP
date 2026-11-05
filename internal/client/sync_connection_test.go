package client

import (
	"testing"
	"time"
)

func TestUsageRefreshDoesNotCancelPendingConnection(t *testing.T) {
	c, engine, _ := setupClient(t)
	engine.mu.Lock()
	engine.blocked = make(chan struct{})
	ready := engine.blocked
	engine.mu.Unlock()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connecting")
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	for i := 0; i < 3; i++ {
		if err := c.Refresh(); err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	changed := c.generation != generation
	c.mu.Unlock()
	if changed {
		t.Fatal("ordinary profile refresh cancelled in-flight connection")
	}
	close(ready)
	waitState(t, c, "connected")
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.calls) != 1 {
		t.Fatalf("connection retried: %v", engine.calls)
	}
}

func TestRefreshPreservesFailureUntilUserActs(t *testing.T) {
	c, engine, _ := setupClient(t)
	engine.mu.Lock()
	engine.fail = true
	engine.mu.Unlock()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "failed")
	before := c.View().LastError
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := c.View(); got.RunState != "failed" || got.LastError != before || got.ConnectionIntent {
		t.Fatalf("refresh concealed connection failure: %+v", got)
	}
	p := c.saved.Profile
	p.Subscription.ExpiresAt = time.Now().Add(-time.Second)
	if err := c.cacheUnavailable(c.api, p); err != nil {
		t.Fatal(err)
	}
	if got := c.View(); got.RunState != "failed" || got.LastError != before {
		t.Fatal("unavailable profile cleared failure")
	}
}

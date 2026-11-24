package client

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Disconnect revokes intent before routeMu becomes available. During that
// interval the engine can still be alive, but it no longer accepts delay work.
func TestDelayRejectedWhileDisconnectWaitsForRoute(t *testing.T) {
	c, original, _ := setupClient(t)
	e := &measuredSwitchEngine{fakeEngine: original, delay: func(context.Context, string) (int64, error) { return 23, nil }}
	c.engine = e
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	awaitConnectionDelay(t, c, 23)
	c.routeMu.Lock()
	var once sync.Once
	unlock := func() { once.Do(c.routeMu.Unlock) }
	defer unlock()
	done := make(chan error, 1)
	go func() { done <- c.Disconnect() }()
	deadline := time.Now().Add(time.Second)
	for c.View().ConnectionIntent && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.View().ConnectionIntent {
		t.Fatal("disconnect did not revoke intent")
	}
	err := c.MeasureDelay()
	unlock()
	if stopErr := <-done; stopErr != nil {
		t.Fatal(stopErr)
	}
	if err == nil || e.measures.Load() != 1 {
		t.Fatal("delay request started after disconnect revoked intent", err, e.measures.Load())
	}
	if v := c.View(); v.Metrics.DelayPending || v.Metrics.DelayMS != nil {
		t.Fatal("disconnected view retained delay work")
	}
}

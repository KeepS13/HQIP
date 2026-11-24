package client

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

type measuredSwitchEngine struct {
	*fakeEngine
	delay    func(context.Context, string) (int64, error)
	measures atomic.Int32
	overlap  atomic.Bool
}

func (e *measuredSwitchEngine) Apply(ctx context.Context, p Profile, b rules.Bundle, id, mode string, tun bool, server string) error {
	if e.Alive() {
		e.overlap.Store(true)
	}
	return e.fakeEngine.Apply(ctx, p, b, id, mode, tun, server)
}

func (e *measuredSwitchEngine) Delay(ctx context.Context) (int64, error) {
	e.measures.Add(1)
	e.mu.Lock()
	node := e.calls[len(e.calls)-1]
	e.mu.Unlock()
	return e.delay(ctx, node)
}

func awaitConnectionDelay(t *testing.T, c *Controller, expected int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m := c.View().Metrics
		if !m.DelayPending && m.DelayMS != nil && *m.DelayMS == expected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("current connection delay not published", c.View().Metrics)
}

func TestConnectionAutomaticallyMeasuresOnlyItsOwnDelayOnce(t *testing.T) {
	c, original, _ := setupClient(t)
	e := &measuredSwitchEngine{fakeEngine: original, delay: func(_ context.Context, id string) (int64, error) {
		if id == "A" {
			return 31, nil
		}
		return 47, nil
	}}
	c.engine = e
	var probes atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { probes.Add(1); return 5, nil }
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	awaitConnectionDelay(t, c, 31)
	if err := c.Select("B"); err != nil {
		t.Fatal(err)
	}
	awaitConnectionDelay(t, c, 47)
	if e.measures.Load() != 2 || e.overlap.Load() || probes.Load() != 0 || c.View().ActualNodeID != "B" {
		t.Fatal("automatic test scope or disconnect-before-connect order violated")
	}
	// Idempotent commands and ordinary imports never repeat the automatic test.
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Select("B"); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if e.measures.Load() != 2 {
		t.Fatal("ordinary command repeated automatic delay")
	}
	if err := c.MeasureDelay(); err != nil {
		t.Fatal(err)
	}
	if e.measures.Load() != 3 {
		t.Fatal("manual measurement not available")
	}
}

func TestLateAutomaticDelayCannotOverwriteNewNode(t *testing.T) {
	c, original, _ := setupClient(t)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := &measuredSwitchEngine{fakeEngine: original, delay: func(ctx context.Context, id string) (int64, error) {
		if id == "A" {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release // Deliberately return success after cancellation.
			return 999, nil
		}
		return 42, nil
	}}
	c.engine = e
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	defer unlock()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, entered, "initial delay was not automatic")
	if err := c.Select("B"); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, canceled, "switch did not cancel old delay")
	awaitConnectionDelay(t, c, 42)
	unlock()
	settled := make(chan struct{})
	go func() { c.workers.Wait(); close(settled) }()
	awaitClosedSignal(t, settled, "old delay did not finish")
	if m := c.View().Metrics; m.DelayMS == nil || *m.DelayMS != 42 {
		t.Fatal("late A delay replaced B's result")
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m := c.View().Metrics; m.DelayPending || m.DelayMS != nil {
		t.Fatal("disconnected client displays old delay")
	}
}

func TestAutomaticDelayFailureKeepsConnectionAndDoesNotRetry(t *testing.T) {
	c, original, _ := setupClient(t)
	e := &measuredSwitchEngine{fakeEngine: original, delay: func(context.Context, string) (int64, error) {
		return 0, errors.New("test endpoint unavailable")
	}}
	c.engine = e
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	deadline := time.Now().Add(time.Second)
	for c.View().Metrics.DelayPending && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	v := c.View()
	if e.measures.Load() != 1 || v.Metrics.DelayMS != nil || v.Metrics.DelayPending || v.RunState != "connected" || v.LastError != "" {
		t.Fatal("delay failure changed connection or retried automatically", v.Status)
	}
}

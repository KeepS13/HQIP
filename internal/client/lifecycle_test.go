package client

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

type closeReportTransport struct {
	base      http.RoundTripper
	started   chan struct{}
	canceled  chan struct{}
	release   chan struct{}
	once      sync.Once
	completed atomic.Bool
}

func (p *closeReportTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/api/v3/client/status" {
		return p.base.RoundTrip(r)
	}
	p.once.Do(func() { close(p.started) })
	<-r.Context().Done()
	close(p.canceled)
	<-p.release // Make cancellation and worker completion separately observable.
	p.completed.Store(true)
	return nil, r.Context().Err()
}

func awaitClosedSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

func TestCloseJoinsSubscriptionReportBeforeReturning(t *testing.T) {
	c, engine, _ := subscriptionClient(t)
	transport := &closeReportTransport{base: c.api.http.Transport, started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	c.api.http.Transport = transport
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(transport.release) }) }
	t.Cleanup(release)
	if err := c.SelectSubscription("b"); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, transport.started, "subscription status report did not start")
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	awaitClosedSignal(t, transport.canceled, "Close did not cancel status reporting")
	select {
	case <-closed:
		t.Fatal("Close returned while the status worker could still write credentials")
	default:
	}
	release()
	awaitClosedSignal(t, closed, "Close did not join status reporting")
	if !transport.completed.Load() {
		t.Fatal("status worker outlived Close")
	}
	// Closing also rejects late frontend requests, so the session directory can
	// immediately be removed without a pending report recreating session.dat.
	if err := os.RemoveAll(c.dir); err != nil {
		t.Fatal(err)
	}
	if c.launch(c.report) || c.launch(c.applyLatest) {
		t.Fatal("accepted background work after Close")
	}
	c.report()
	if err := c.Connect(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect after Close: %v", err)
	}
	if err := c.SelectSubscription("a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("subscription selection after Close: %v", err)
	}
	if err := c.Sync(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync after Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "session.dat")); !os.IsNotExist(err) {
		t.Fatalf("closed controller wrote credentials: %v", err)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.subscriptions) != 0 {
		t.Fatal("off-state shutdown started an engine")
	}
}

type closeApplyEngine struct {
	fakeEngine
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	count    atomic.Int32
}

func (e *closeApplyEngine) Apply(ctx context.Context, _ Profile, _ rules.Bundle, _, _ string, _ bool, _ string) error {
	e.count.Add(1)
	close(e.started)
	<-ctx.Done()
	close(e.canceled)
	<-e.release
	return ctx.Err()
}

func TestCloseJoinsApplyAndDiscardsQueuedApply(t *testing.T) {
	c, _, _ := setupClient(t)
	engine := &closeApplyEngine{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	c.engine = engine
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(engine.release) }) }
	t.Cleanup(release)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, engine.started, "first Apply did not start")
	// A real mode change queues Apply; duplicate Connect is intentionally a no-op.
	if err := c.SetMode("global", false); err != nil {
		t.Fatal(err)
	}
	awaitClosedSignal(t, engine.canceled, "superseded Apply was not canceled")
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	awaitClosedSignal(t, c.workCtx.Done(), "Close did not begin shutdown")
	select {
	case <-closed:
		t.Fatal("Close returned before the running Apply finished")
	default:
	}
	release()
	awaitClosedSignal(t, closed, "Close did not wait for queued work")
	if engine.count.Load() != 1 || engine.Alive() {
		t.Fatal("queued Apply ran during shutdown")
	}
	c.Close() // Closing from both Wails OnShutdown and defer remains safe.
}

func TestCloseCancelsRunWithoutCallerCancellation(t *testing.T) {
	c, err := New(t.TempDir(), fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	done := make(chan struct{})
	if !c.launch(func() { c.Run(context.Background()); close(done) }) {
		t.Fatal("could not start polling")
	}
	c.Close()
	awaitClosedSignal(t, done, "polling survived Close with a background parent")
}

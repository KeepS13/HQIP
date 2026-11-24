package client

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type probeNoHTTP struct{ calls atomic.Int32 }

func (p *probeNoHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls.Add(1)
	return nil, errors.New("handshake test must not call account APIs")
}

func selectedProbeClient(t *testing.T) (*Controller, *fakeEngine, *probeNoHTTP) {
	t.Helper()
	c, engine, _ := setupClient(t)
	c.mu.Lock()
	c.saved.Profile = catalogProfile()
	c.populateLocked(c.saved.Profile)
	c.view.RunState = "disconnected"
	c.mu.Unlock()
	transport := &probeNoHTTP{}
	c.api.http.Transport = transport
	if err := c.SelectSubscriptionNode("first", "shared"); err != nil {
		t.Fatal(err)
	}
	return c, engine, transport
}

func TestSelectedNodeProbeOnlyOneAndPreservesOtherResults(t *testing.T) {
	c, engine, transport := selectedProbeClient(t)
	var calls atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { calls.Add(1); return 18, nil }
	if err := c.TestNodeDelays(); err != nil {
		t.Fatal(err)
	}
	waitNodeTests(t, c)
	calls.Store(0)
	before := c.View().Status
	c.nodeProbe = func(_ context.Context, node Node) (int64, error) {
		calls.Add(1)
		if node.ID != "shared" {
			t.Errorf("probed unselected node: %s", node.ID)
		}
		return 31, nil
	}
	if err := c.TestSelectedNodeDelay("first", "shared"); err != nil {
		t.Fatal(err)
	}
	v := waitNodeTests(t, c)
	if calls.Load() != 1 || len(v.Results) != 2 || v.NodeID != "shared" || v.SubscriptionID != "first" {
		t.Fatal(calls.Load(), v)
	}
	for _, r := range v.Results {
		want := int64(18)
		if r.NodeID == "shared" {
			want = 31
		}
		if r.DelayMS != want || r.Status != "success" {
			t.Fatal("lost cached result", r)
		}
	}
	if c.View().Status != before || transport.calls.Load() != 0 || len(engine.calls) != 0 {
		t.Fatal("probe changed connection or contacted account server")
	}
}

func TestSelectedNodeProbeCoalescesAndKeepsOriginalTarget(t *testing.T) {
	c, engine, transport := selectedProbeClient(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c.nodeProbe = func(ctx context.Context, node Node) (int64, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if err := c.TestSelectedNodeDelay("first", "shared"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := c.TestSelectedNodeDelay("first", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := c.SelectSubscriptionNode("third", "alias"); err != nil {
		t.Fatal(err)
	}
	c.CancelSelectedNodeDelay("third", "alias")
	if !c.View().NodeTests.Pending {
		t.Fatal("another selection cancelled this job")
	}
	close(release)
	v := waitNodeTests(t, c)
	if calls.Load() != 1 || v.Results[0].NodeID != "shared" || v.Results[0].DelayMS != 42 {
		t.Fatal(calls.Load(), v)
	}
	if c.View().DesiredNodeID != "alias" || transport.calls.Load() != 0 || len(engine.calls) != 0 {
		t.Fatal("test affected local selection/connection")
	}
}

func TestSelectedProbeRejectsInvalidTargetsAndRevokesAccess(t *testing.T) {
	for _, target := range [][2]string{{"", ""}, {"first", "alias"}, {"second", "shared"}} {
		c, _, _ := selectedProbeClient(t)
		if err := c.TestSelectedNodeDelay(target[0], target[1]); err == nil {
			t.Fatal("accepted stale or invalid choice", target)
		}
	}
	for _, mutation := range []string{"expired", "removed", "endpoint", "logout", "mode", "quota"} {
		t.Run(mutation, func(t *testing.T) {
			c, _, _ := selectedProbeClient(t)
			started, stopped := make(chan struct{}), make(chan struct{})
			c.nodeProbe = func(ctx context.Context, _ Node) (int64, error) {
				close(started)
				<-ctx.Done()
				close(stopped)
				return 999, nil
			}
			if err := c.TestSelectedNodeDelay("first", "shared"); err != nil {
				t.Fatal(err)
			}
			<-started
			c.mu.Lock()
			switch mutation {
			case "expired":
				c.saved.Profile.Subscriptions[0].ExpiresAt = time.Now().Add(-time.Second)
			case "removed":
				c.saved.Profile.Subscriptions[0].Status = "removed"
			case "endpoint":
				c.saved.Profile.AvailableNodes[0].Port++
			case "logout":
				c.view.LoggedIn = false
			case "mode":
				c.saved.Mode = "changed"
			case "quota":
				zero := int64(0)
				c.saved.Profile.Subscriptions[0].QuotaBytes = &zero
			}
			c.mu.Unlock()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("scope loss did not cancel")
			}
			if v := waitNodeTests(t, c); v.Pending || len(v.Results) != 0 {
				t.Fatal("stale result", v)
			}
		})
	}
}

func TestSingleProbeReplacesCatalogWithoutRestartingAutomaticScan(t *testing.T) {
	c, _, _ := selectedProbeClient(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	c.nodeProbe = func(ctx context.Context, _ Node) (int64, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return 999, nil
	}
	if err := c.TestNodeDelays(); err != nil {
		t.Fatal(err)
	}
	<-started
	var single atomic.Int32
	c.mu.Lock()
	c.nodeProbe = func(context.Context, Node) (int64, error) { single.Add(1); return 27, nil }
	c.mu.Unlock()
	if err := c.TestSelectedNodeDelay("first", "shared"); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	v := waitNodeTests(t, c)
	if single.Load() != 1 || v.NodeID != "shared" || v.Results[0].DelayMS != 27 {
		t.Fatal(single.Load(), v)
	}
	if err := c.TestNodeDelays(); err != nil {
		t.Fatal(err)
	}
	c.CancelSelectedNodeDelay("first", "shared")
	waitNodeTests(t, c)
	if c.View().NodeTests.NodeID != "" {
		t.Fatal("global command lost legacy scope")
	}
}

func TestSelectedProbeCancelAndClose(t *testing.T) {
	c, _, _ := selectedProbeClient(t)
	started := make(chan struct{}, 2)
	c.nodeProbe = func(ctx context.Context, _ Node) (int64, error) {
		started <- struct{}{}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if err := c.TestSelectedNodeDelay("first", "shared"); err != nil {
		t.Fatal(err)
	}
	<-started
	c.CancelSelectedNodeDelay("first", "shared")
	if v := c.View().NodeTests; v.Pending || v.Results[0].Status != "cancelled" {
		t.Fatal(v)
	}
	if err := c.TestSelectedNodeDelay("first", "shared"); err != nil {
		t.Fatal(err)
	}
	<-started
	c.Close()
	if err := c.TestSelectedNodeDelay("first", "shared"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSelectedProbeLegacyCatalogAndConnectedState(t *testing.T) {
	c, engine, _ := setupClient(t)
	if err := c.SelectSubscriptionNode("sub-a", "B"); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.view.RunState, c.view.ConnectionIntent = "connected", true
	c.view.ActualNodeID, c.view.ActualSubscriptionID = "A", "sub-a"
	c.mu.Unlock()
	before := c.View().Status
	var calls atomic.Int32
	c.nodeProbe = func(_ context.Context, node Node) (int64, error) {
		calls.Add(1)
		if node.ID != "B" {
			t.Errorf("tested authorized default instead of displayed draft: %s", node.ID)
		}
		return 26, nil
	}
	if err := c.TestSelectedNodeDelay("sub-a", "B"); err != nil {
		t.Fatal(err)
	}
	waitNodeTests(t, c)
	if calls.Load() != 1 || c.View().Status != before || len(engine.calls) != 0 {
		t.Fatal("test changed active connection")
	}
}

package client

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestBackgroundSyncNeverAutomaticallyProbes(t *testing.T) {
	c, _, _ := setupClient(t)
	var probes atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { probes.Add(1); return 19, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	for i := 0; i < 3; i++ {
		if err := c.Refresh(); err != nil {
			t.Fatal(err)
		}
		if err := c.SelectSubscriptionNode("sub-a", "B"); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	if probes.Load() != 0 || len(c.View().NodeTests.Results) != 0 {
		t.Fatal("login/sync/selection started probes", probes.Load())
	}
}

func TestRepeatedLocalChoicesDoNotProbeAndPublishWholePair(t *testing.T) {
	c, engine, transport := selectedProbeClient(t)
	var probes atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { probes.Add(1); return 19, nil }
	for i := 0; i < 20; i++ {
		for _, choice := range [][2]string{{"first", "shared"}, {"third", "alias"}} {
			if err := c.SelectSubscriptionNode(choice[0], choice[1]); err != nil {
				t.Fatal(err)
			}
			v := c.View()
			if v.Subscription == nil || v.Subscription.ID != choice[0] || v.DesiredSubscriptionID != choice[0] || v.DesiredNodeID != choice[1] {
				t.Fatal("snapshot mixed the old subscription and new node")
			}
			if v.NodeTests.Pending || v.SelectionPending || v.ConnectionIntent {
				t.Fatal("choice started unrelated work")
			}
		}
	}
	if probes.Load() != 0 || transport.calls.Load() != 0 || len(engine.calls) != 0 {
		t.Fatal("choice did network/engine work")
	}
	if err := c.TestSelectedNodeDelay("third", "alias"); err != nil {
		t.Fatal(err)
	}
	waitNodeTests(t, c)
	if probes.Load() != 1 {
		t.Fatal("manual probe did not test exactly once", probes.Load())
	}
}

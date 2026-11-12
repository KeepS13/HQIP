package client

import (
	"context"
	"strings"
	"testing"
)

func TestRemovedNodeUsesAutomaticCandidateDuringRecovery(t *testing.T) {
	f := newRecoveryFixture(t)
	r := f.recovery()
	f.mu.Lock()
	f.profile.Nodes = []Node{{ID: "B", Ready: true}}
	f.profile.DefaultNodeID = "B"
	f.profile.Revision++
	f.mu.Unlock()
	if _, err := f.c.recoveryAttempt(r); err != nil {
		t.Fatal(err)
	}
	waitState(t, f.c, "connected")
	view := f.c.View()
	if view.ActualNodeID != "B" || view.DesiredNodeID != "B" || !view.ConnectionIntent || r.ctx.Err() == nil {
		t.Fatalf("recovery did not hand over to replacement: %+v", view.Status)
	}
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	if len(f.e.calls) != 2 || f.e.calls[0] != "A" || f.e.calls[1] != "B" {
		t.Fatalf("unexpected routes: %v", f.e.calls)
	}
}

func TestRemovedNodeSwitchesOnlyWhileConnectionRequested(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnected", true: "connected"}[connected], func(t *testing.T) {
			c, engine, set := setupClient(t)
			if connected {
				if err := c.Connect(); err != nil {
					t.Fatal(err)
				}
				waitState(t, c, "connected")
			}
			set(Profile{Revision: 2, DefaultNodeID: "B", Nodes: []Node{{ID: "B", Ready: true}}})
			if err := c.Refresh(); err != nil {
				t.Fatal(err)
			}
			if connected {
				// Wait for applyLatest, since Sync intentionally schedules it.
				c.applyLatest()
				waitState(t, c, "connected")
			}
			view := c.View()
			if view.DesiredNodeID != "B" || view.ConnectionIntent != connected || connected && view.ActualNodeID != "B" {
				t.Fatalf("unexpected replacement state: %+v", view.Status)
			}
			engine.mu.Lock()
			defer engine.mu.Unlock()
			wantCalls := 0
			if connected {
				wantCalls = 2
			}
			if len(engine.calls) != wantCalls {
				t.Fatalf("unexpected connection attempts: %v", engine.calls)
			}
		})
	}
}

func TestAutomaticRankingChangesPreserveCurrentConnection(t *testing.T) {
	c, engine, set := setupClient(t)
	if err := c.Select("B"); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	c.mu.Lock()
	p := c.saved.Profile
	generation := c.generation
	c.mu.Unlock()
	for _, suggested := range []string{"B", "A", "", "B"} {
		p.DefaultNodeID = suggested
		set(p)
		if err := c.Refresh(); err != nil {
			t.Fatal(err)
		}
		if view := c.View(); view.DesiredNodeID != "B" || view.ActualNodeID != "B" || !view.ConnectionIntent {
			t.Fatalf("ranking moved the current choice: %+v", view.Status)
		}
	}
	c.mu.Lock()
	changed := c.generation != generation
	c.mu.Unlock()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if changed || len(engine.calls) != 1 {
		t.Fatal("telemetry restarted the connection", engine.calls)
	}
}

func TestAutomaticSelectionWithNoCandidateStopsRemovedRoute(t *testing.T) {
	c, engine, set := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	// B is manually selectable, but the service has no eligible auto candidate.
	set(Profile{Revision: 2, Nodes: []Node{{ID: "B", Ready: true}}})
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "failed")
	view := c.View()
	if view.ConnectionIntent || view.DesiredNodeID != "" || view.ActualNodeID != "" || !strings.Contains(view.LastError, "暂无可用节点") || engine.Alive() {
		t.Fatalf("missing candidate retained route: %+v", view.Status)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.calls) != 1 {
		t.Fatal("connected arbitrary fallback", engine.calls)
	}
}

func TestAutomaticReplacementRespectsConcurrentDisconnect(t *testing.T) {
	c, engine, set := setupClient(t)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	engine.mu.Lock()
	engine.blocked = make(chan struct{})
	engine.mu.Unlock()
	set(Profile{Revision: 2, DefaultNodeID: "B", Nodes: []Node{{ID: "B", Ready: true}}})
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connecting")
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c.applyMu.Lock()
	c.applyMu.Unlock()
	if view := c.View(); view.ConnectionIntent || view.ActualNodeID != "" || engine.Alive() {
		t.Fatalf("automatic replacement overrode disconnect: %+v", view.Status)
	}
}

func TestFirstSubscriptionChoiceUsesOnlyEligibleSuggestion(t *testing.T) {
	c, _, _ := setupClient(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.saved.Profile
	p.SubscriptionID = "new-subscription"
	p.DefaultNodeID = "B"
	if got := c.rememberedNodeLocked(p); got != "B" {
		t.Fatal("first choice did not use server suggestion", got)
	}
	p.DefaultNodeID = "missing"
	if got := c.rememberedNodeLocked(p); got != "" {
		t.Fatal("first choice used unauthorized suggestion", got)
	}
}

func TestAvailableNodeClearsOnlyUnavailableWarning(t *testing.T) {
	for _, method := range []string{"automatic", "manual"} {
		for _, unrelatedError := range []string{"", "规则校验失败，已停止代理"} {
			t.Run(method+"/"+unrelatedError, func(t *testing.T) {
				c, engine, set := setupClient(t)
				set(Profile{Revision: 2, Nodes: []Node{{ID: "B", Ready: true}}})
				if err := c.Refresh(); err != nil {
					t.Fatal(err)
				}
				if view := c.View(); view.DesiredNodeID != "" || !strings.Contains(view.LastError, "暂无可用节点") {
					t.Fatal("missing candidate did not show warning", view.Status)
				}
				if unrelatedError != "" {
					c.mu.Lock()
					c.view.LastError = unrelatedError
					c.mu.Unlock()
				}
				if method == "automatic" {
					set(Profile{Revision: 3, DefaultNodeID: "B", Nodes: []Node{{ID: "B", Ready: true}}})
					if err := c.Refresh(); err != nil {
						t.Fatal(err)
					}
				} else if err := c.Select("B"); err != nil {
					t.Fatal(err)
				}
				if view := c.View(); view.DesiredNodeID != "B" || view.LastError != unrelatedError || view.ConnectionIntent || view.ActualNodeID != "" {
					t.Fatalf("available choice has wrong warning or connected automatically: %+v", view.Status)
				}
				engine.mu.Lock()
				defer engine.mu.Unlock()
				if len(engine.calls) != 0 {
					t.Fatal("changing a disconnected choice started the engine", engine.calls)
				}
			})
		}
	}
}

package client

import (
	"os"
	"path/filepath"
	"testing"
)

// Duplicate UI/tray requests must not cancel a healthy or recovering session.
func TestDuplicateConnectionCommandsPreserveSession(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		t.Run(map[bool]string{false: "connected", true: "recovering"}[recovering], func(t *testing.T) {
			f := newRecoveryFixture(t)
			if recovering {
				f.recovery()
			}
			c := f.c
			c.mu.Lock()
			generation, recovery := c.generation, c.recovery
			mode, tun := c.saved.Mode, c.saved.TUN
			c.mu.Unlock()
			if err := c.SetMode(mode, tun); err != nil {
				t.Fatal(err)
			}
			if err := c.Connect(); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			changed := c.generation != generation || c.recovery != recovery || !c.recoveryReady
			c.mu.Unlock()
			if changed {
				t.Fatal("repeated commands cancelled the current connection generation")
			}
		})
	}
}

func TestModeChangeStillAppliesAndDisconnectedModeStaysOff(t *testing.T) {
	c, _, _ := setupClient(t)
	if err := c.SetMode("global", true); err != nil {
		t.Fatal(err)
	}
	if c.View().ConnectionIntent {
		t.Fatal("changing mode connected without user intent")
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	c.mu.Lock()
	before := c.generation
	c.mu.Unlock()
	if err := c.SetMode("rule", false); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	changed := c.generation == before+1 && c.saved.Mode == "rule" && !c.saved.TUN
	c.mu.Unlock()
	if !changed {
		t.Fatal("real mode/TUN changes must apply a new configuration")
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "disconnected")
}

func TestDuplicateConnectPreservesPendingApply(t *testing.T) {
	c, engine, _ := setupClient(t)
	engine.mu.Lock()
	engine.blocked = make(chan struct{})
	ready := engine.blocked
	engine.mu.Unlock()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connecting")
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	close(ready)
	waitState(t, c, "connected")
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.calls) != 1 {
		t.Fatalf("duplicate click restarted pending transport: %v", engine.calls)
	}
}

// A failed preference write must leave the running generation and its recovery
// permission untouched; an unwritable staging path simulates a storage failure.
func TestModePersistenceFailurePreservesConnection(t *testing.T) {
	f := newRecoveryFixture(t)
	f.recovery()
	c := f.c
	c.mu.Lock()
	beforeDir, generation, recovery := c.dir, c.generation, c.recovery
	mode, tun := c.saved.Mode, c.saved.TUN
	c.dir = t.TempDir()
	blocked := filepath.Join(c.dir, "session.tmp")
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.dir = beforeDir; c.mu.Unlock() }()
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	next := "global"
	if mode == next {
		next = "rule"
	}
	if err := c.SetMode(next, !tun); err == nil {
		t.Fatal("mode change unexpectedly succeeded with unwritable preferences")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.recovery != recovery || !c.recoveryReady ||
		c.saved.Mode != mode || c.saved.TUN != tun || c.view.Mode != mode || c.view.TUNEnabled != tun {
		t.Fatal("failed preference write changed the current session or visible mode")
	}
}

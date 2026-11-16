package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func enableOfflineFixture(t *testing.T, f *recoveryFixture) time.Time {
	t.Helper()
	f.mu.Lock()
	now := f.now
	for i := range f.profile.Nodes {
		f.profile.Nodes[i].OfflineAdmissionVersion = 1
		f.profile.Nodes[i].OfflineUntil = now.Add(24 * time.Hour)
	}
	f.mu.Unlock()
	if err := f.c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.c.mu.Lock()
	f.c.saved.ServerURL = f.c.api.base
	f.c.rememberOfflineLocked(true)
	err := f.c.persistLocked()
	until := f.c.saved.Offline.Until
	f.c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return until
}

func TestOfflineClientKeepsForwardingAndRestoresWithoutConnectionsAPI(t *testing.T) {
	f := newRecoveryFixture(t)
	until := enableOfflineFixture(t, f)
	f.mu.Lock()
	f.offline = true
	f.now = f.now.Add(5 * time.Minute)
	f.mu.Unlock()
	if err := f.c.Sync(context.Background()); !retryable(err) {
		t.Fatal("outage classification", err)
	}
	f.c.checkAccountConnection(context.Background())
	f.c.accountWatchdogTick(context.Background())
	f.c.checkOfflineForwarding(context.Background())
	if v := f.c.View(); v.RunState != "connected" || !v.ControlUnavailable || f.e.pauses.Load() != 0 {
		t.Fatal("healthy tunnel paused", v.Status)
	}
	if err := f.c.Select("B"); err == nil {
		t.Fatal("offline switch accepted")
	}
	if f.c.View().ActualNodeID != "A" {
		t.Fatal("switch disconnected original")
	}
	r := f.recovery()
	done, err := f.c.recoveryAttempt(r)
	if !done || err != nil {
		t.Fatal("central dependency on recovery", done, err)
	}
	f.c.mu.Lock()
	actual := f.c.saved.Offline.Until
	f.c.mu.Unlock()
	if !actual.Equal(until) {
		t.Fatal("recovery extended deadline")
	}
	if err = f.c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err = f.c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, f.c, "connected")
	f.c.mu.Lock()
	actual = f.c.saved.Offline.Until
	f.c.mu.Unlock()
	if !actual.Equal(until) {
		t.Fatal("manual reconnect extended deadline")
	}
	f.mu.Lock()
	f.now = until
	f.mu.Unlock()
	f.c.accountWatchdogTick(context.Background())
	if v := f.c.View(); v.ConnectionIntent || v.RunState != "failed" {
		t.Fatal("offline deadline not enforced", v.Status)
	}
}

func TestOfflineClientCacheRestartAndClockRollback(t *testing.T) {
	f := newRecoveryFixture(t)
	until := enableOfflineFixture(t, f)
	c := f.c
	c.mu.Lock()
	dir, platform := c.dir, c.platform
	last := c.saved.Offline.LastWall
	c.mu.Unlock()
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	c.Close()
	f.mu.Lock()
	f.offline = true
	f.now = f.now.Add(5 * time.Minute)
	f.mu.Unlock()
	restarted, err := New(dir, platform, f.e)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	// The test platform's existing rules file is created by setupClient/Sync.
	restarted.mu.Lock()
	valid := restarted.offlineValidLocked()
	restarted.mu.Unlock()
	if !valid {
		t.Fatal("authorized encrypted cache lost after restart")
	}
	if restarted.View().ConnectionIntent {
		t.Fatal("restart auto-connected")
	}
	if err = restarted.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, restarted, "connected")
	restarted.mu.Lock()
	actual := restarted.saved.Offline.Until
	restarted.mu.Unlock()
	if !actual.Equal(until) {
		t.Fatal("restart renewed deadline")
	}
	f.mu.Lock()
	f.now = last.Add(-time.Second)
	f.mu.Unlock()
	_ = restarted.Sync(context.Background())
	restarted.accountWatchdogTick(context.Background())
	if restarted.View().ConnectionIntent {
		t.Fatal("clock rollback kept offline access")
	}
}

func TestOfflineNodeFailureAndLateProbeCannotUndoDisconnect(t *testing.T) {
	f := newRecoveryFixture(t)
	enableOfflineFixture(t, f)
	f.mu.Lock()
	f.offline = true
	f.mu.Unlock()
	_ = f.c.Sync(context.Background())
	r := f.recovery()
	f.e.verify = func(context.Context) error { return temporary(errors.New("node offline")) }
	if done, err := f.c.recoveryAttempt(r); done || !retryable(err) {
		t.Fatal("dead node claimed connected")
	}
	f.e.verify = func(context.Context) error { _ = f.c.Disconnect(); return nil }
	_, _ = f.c.recoveryAttempt(r)
	if v := f.c.View(); v.ConnectionIntent || v.RunState != "disconnected" {
		t.Fatal("late probe undid manual disconnect", v.Status)
	}
}

func TestOfflinePolicyRefreshDoesNotReapplyConfiguration(t *testing.T) {
	f := newRecoveryFixture(t)
	enableOfflineFixture(t, f)
	f.mu.Lock()
	before := f.profile
	after := before
	after.Nodes = append([]Node(nil), before.Nodes...)
	after.Nodes[0].OfflineUntil = after.Nodes[0].OfflineUntil.Add(time.Hour)
	f.mu.Unlock()
	if connectionConfigurationChanged(before, after, "A") {
		t.Fatal("permit renewal reapplies transport")
	}
}

func TestOfflineTerminalFailuresInvalidatePersistedPermit(t *testing.T) {
	for _, failure := range []string{"selection_unauthorized", "selection_protocol", "sync_protocol", "node_protocol"} {
		t.Run(failure, func(t *testing.T) {
			f := newRecoveryFixture(t)
			enableOfflineFixture(t, f)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "selection_unauthorized" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = w.Write([]byte("{"))
			}))
			defer srv.Close()
			api, err := NewAPI(srv.URL, f.c.api.Tokens())
			if err != nil {
				t.Fatal(err)
			}
			f.c.mu.Lock()
			f.c.api = api
			dir, platform := f.c.dir, f.c.platform
			f.c.mu.Unlock()
			switch failure {
			case "selection_unauthorized", "selection_protocol":
				err = f.c.Select("B")
			case "sync_protocol":
				err = f.c.Sync(context.Background())
			case "node_protocol":
				f.e.verify = func(context.Context) error { return errors.New("invalid TLS certificate") }
				f.c.checkOfflineForwarding(context.Background())
				err = errors.New("expected node rejection")
			}
			if err == nil || f.c.View().ConnectionIntent {
				t.Fatal("terminal response retained connection", err)
			}
			f.c.mu.Lock()
			cached := f.c.saved.Offline != nil
			loggedIn := f.c.api != nil
			f.c.mu.Unlock()
			if cached || failure == "selection_unauthorized" && loggedIn {
				t.Fatal("terminal response retained authorization")
			}
			f.c.Close()
			restarted, err := New(dir, platform, f.e)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if restarted.saved.Offline != nil {
				t.Fatal("restart resurrected rejected authorization")
			}
		})
	}
}

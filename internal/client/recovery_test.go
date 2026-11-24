package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

type recoveryEngine struct {
	*fakeEngine
	verify  func(context.Context) error
	pause   func(context.Context, bool) error
	probes  atomic.Int32
	pauses  atomic.Int32
	resumes atomic.Int32
}

func (e *recoveryEngine) VerifyCurrent(ctx context.Context, _ string) error {
	e.probes.Add(1)
	if e.verify != nil {
		return e.verify(ctx)
	}
	return nil
}
func (e *recoveryEngine) SetSuspended(ctx context.Context, paused bool) error {
	if paused {
		e.pauses.Add(1)
	} else {
		e.resumes.Add(1)
	}
	if e.pause != nil {
		return e.pause(ctx, paused)
	}
	return nil
}

type recoveryFixture struct {
	c       *Controller
	e       *recoveryEngine
	now     time.Time
	mu      sync.Mutex
	profile Profile
	state   Connections
	offline bool
	drain   int
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	c, engine, _ := setupClient(t)
	f := &recoveryFixture{c: c, e: &recoveryEngine{fakeEngine: engine}, now: time.Now(), profile: c.saved.Profile}
	f.state = Connections{SubscriptionID: f.profile.SubscriptionID, SelectionRevision: f.profile.SelectionRevision, MaxConnections: 2}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.offline {
			w.WriteHeader(503)
			return
		}
		switch r.URL.Path {
		case "/api/v3/client/profile", "/api/v3/client/subscription":
			json.NewEncoder(w).Encode(f.profile)
		case f.profile.Rules.URL:
			data, _ := rules.Canonical(c.bundle)
			w.Write(data)
		case "/api/v3/client/connections":
			json.NewEncoder(w).Encode(f.state)
		case "/api/v3/client/connections/drain":
			f.drain++
			w.WriteHeader(204)
		default:
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(srv.Close)
	api, _ := NewAPI(srv.URL, Tokens{AccessToken: "test", RefreshToken: "test", AccessExpiresAt: time.Now().Add(time.Hour)})
	c.api, c.engine, c.now = api, f.e, func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	// Close before the test API, including canceled background retries.
	t.Cleanup(c.Close)
	return f
}

// Install a recovery generation without starting its scheduler. Tests drive
// attempts and the authorization clock separately, with no wall-clock backoff.
func (f *recoveryFixture) recovery() *recoveryRun {
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithCancel(c.workCtx)
	r := &recoveryRun{c.api, c.generation, c.selectionGeneration, c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID, ctx, cancel}
	c.recovery = r
	c.view.RunState = "recovering"
	c.view.ActualNodeID, c.view.ActualSubscriptionID = "", ""
	return r
}
func (f *recoveryFixture) lease() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Connections = []Connection{{SubscriptionID: f.profile.SubscriptionID, NodeID: "A", State: "active", ExpiresAt: f.now.Add(time.Minute)}}
}
func assertSilent(t *testing.T, c *Controller) {
	t.Helper()
	v := c.View()
	if !v.ConnectionIntent || v.RunState != "recovering" || v.LastError != "" || v.SyncError != "" {
		t.Fatalf("not silent: %+v", v.Status)
	}
}

func TestRecoveryRequiresHTTPSAndCurrentLeaseWithoutRestart(t *testing.T) {
	f := newRecoveryFixture(t)
	r := f.recovery()
	c := f.c
	before := c.View().Metrics.ConnectedAt
	f.e.verify = func(context.Context) error { return temporary(errors.New("injected UDP timeout")) }
	done, err := c.recoveryAttempt(r)
	if done || !retryable(err) {
		t.Fatalf("timeout: %v %v", done, err)
	}
	assertSilent(t, c)
	f.e.verify = nil
	done, err = c.recoveryAttempt(r)
	if done || !retryable(err) {
		t.Fatalf("missing lease: %v %v", done, err)
	}
	f.mu.Lock()
	f.state.Connections = []Connection{{SubscriptionID: "other", NodeID: "A", State: "active", ExpiresAt: f.now.Add(time.Minute)}}
	f.mu.Unlock()
	done, err = c.recoveryAttempt(r)
	if done || !retryable(err) {
		t.Fatal("accepted wrong subscription lease")
	}
	f.lease()
	done, err = c.recoveryAttempt(r)
	if !done || err != nil {
		t.Fatalf("recovery: %v", err)
	}
	v := c.View()
	if v.RunState != "connected" || v.ActualNodeID != "A" || !v.Metrics.ConnectedAt.Equal(*before) {
		t.Fatal("recovery changed connection")
	}
	f.e.mu.Lock()
	calls := len(f.e.calls)
	f.e.mu.Unlock()
	f.mu.Lock()
	drain := f.drain
	f.mu.Unlock()
	if calls != 1 || drain != 1 {
		t.Fatalf("recovery restarted/drained: %d/%d", calls, drain)
	}
}

func TestRecoveryWaitsForCapacityAndReadiness(t *testing.T) {
	f := newRecoveryFixture(t)
	r := f.recovery()
	f.mu.Lock()
	f.state.Occupied = 2
	f.mu.Unlock()
	_, err := f.c.recoveryAttempt(r)
	if !retryable(err) || f.e.probes.Load() != 1 {
		t.Fatal("full IP count prevented ingress admission or resumed without a lease")
	}
	f.mu.Lock()
	f.state.Occupied = 0
	f.profile.Nodes[0].Ready = false
	f.mu.Unlock()
	_, err = f.c.recoveryAttempt(r)
	if !retryable(err) || f.e.probes.Load() != 1 {
		t.Fatal("unready node rebuilt transport")
	}
	assertSilent(t, f.c)
}

func TestOfflineDeadlinePausesThenVerifiesBeforeResume(t *testing.T) {
	f := newRecoveryFixture(t)
	r := f.recovery()
	f.mu.Lock()
	f.offline = true
	f.now = f.now.Add(119 * time.Second)
	f.mu.Unlock()
	_, err := f.c.recoveryAttempt(r)
	if !retryable(err) {
		t.Fatal(err)
	}
	f.c.accountWatchdogTick(context.Background())
	if f.e.pauses.Load() != 0 {
		t.Fatal("paused early")
	}
	f.mu.Lock()
	f.now = f.now.Add(2 * time.Second)
	f.mu.Unlock()
	f.c.accountWatchdogTick(context.Background())
	f.c.accountWatchdogTick(context.Background())
	if f.e.pauses.Load() != 1 || !f.e.Alive() {
		t.Fatal("deadline did not preserve paused core")
	}
	assertSilent(t, f.c)
	f.mu.Lock()
	f.offline = false
	f.mu.Unlock()
	f.e.verify = func(context.Context) error { return temporary(errors.New("HTTPS timeout")) }
	_, err = f.c.recoveryAttempt(r)
	if !retryable(err) || f.e.resumes.Load() != 0 {
		t.Fatal("resumed without verified node")
	}
	f.e.verify = nil
	f.lease()
	done, err := f.c.recoveryAttempt(r)
	if !done || err != nil || f.e.resumes.Load() != 1 {
		t.Fatalf("did not resume: %v", err)
	}
}

func TestRecoveryPauseFailureAndSubscriptionExpiryStop(t *testing.T) {
	for _, failure := range []string{"pause", "expiry"} {
		t.Run(failure, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.recovery()
			f.mu.Lock()
			f.now = f.now.Add(121 * time.Second)
			f.mu.Unlock()
			if failure == "pause" {
				f.e.pause = func(context.Context, bool) error { return errors.New("injected") }
			} else {
				f.c.mu.Lock()
				f.c.view.Subscription.ExpiresAt = time.Now().Add(-time.Minute)
				f.c.mu.Unlock()
			}
			f.c.accountWatchdogTick(context.Background())
			v := f.c.View()
			if v.ConnectionIntent || v.RunState != "failed" || v.LastError == "" || f.e.Alive() {
				t.Fatal("terminal fault did not stop")
			}
		})
	}
}

func TestRecoveryLateProbeCannotResurrectAfterUserAction(t *testing.T) {
	for _, action := range []string{"disconnect", "select", "subscription", "logout", "close"} {
		t.Run(action, func(t *testing.T) {
			f := newRecoveryFixture(t)
			r := f.recovery()
			entered, release := make(chan struct{}), make(chan struct{})
			f.e.verify = func(context.Context) error { close(entered); <-release; return errors.New("late terminal error") }
			done := make(chan struct{})
			go func() {
				_, err := f.c.recoveryAttempt(r)
				if err != nil {
					f.c.stopRecovery(r, err.Error())
				}
				close(done)
			}()
			<-entered
			switch action {
			case "disconnect":
				_ = f.c.Disconnect()
			case "select":
				_ = f.c.Select("B")
			case "subscription":
				_ = f.c.SelectSubscriptionNode("sub-a", "B")
			case "logout":
				_ = f.c.Logout()
			case "close":
				f.c.Close()
			}
			close(release)
			<-done
			if action == "select" || action == "subscription" {
				waitState(t, f.c, "connected")
				if f.c.View().DesiredNodeID != "B" || f.c.View().ActualNodeID != "B" {
					t.Fatal("old recovery response replaced the new connection")
				}
			} else if f.c.View().ConnectionIntent || f.e.Alive() {
				t.Fatal("late response revived connection")
			}
			if strings.Contains(f.c.View().LastError, "late") {
				t.Fatal("old failure surfaced")
			}
		})
	}
}

func TestRepeatedLeaseChecksStartOneCancelableRecovery(t *testing.T) {
	f := newRecoveryFixture(t)
	entered := make(chan struct{})
	var once sync.Once
	f.e.verify = func(ctx context.Context) error { once.Do(func() { close(entered) }); <-ctx.Done(); return ctx.Err() }
	f.c.checkAccountConnection(context.Background())
	<-entered
	for range 6 {
		f.c.checkAccountConnection(context.Background())
	}
	assertSilent(t, f.c)
	if f.e.probes.Load() != 1 {
		t.Fatal("overlapping recovery probes")
	}
	_ = f.c.Disconnect()
	waitState(t, f.c, "disconnected")
}

func TestRecoveryBackoffAndDiagnosticRotation(t *testing.T) {
	for i, want := range []time.Duration{2, 5, 10, 20, 30, 30, 30} {
		for _, random := range []float64{0, .5, 1} {
			got := recoveryDelay(i, random)
			expected := time.Duration(float64(want*time.Second) * (.8 + .4*random))
			if got != expected {
				t.Fatal(got, expected)
			}
		}
	}
	c := &Controller{dir: t.TempDir()}
	for _, name := range []string{"recovery.log", "recovery.log.1", "recovery.log.2"} {
		if err := os.WriteFile(filepath.Join(c.dir, name), make([]byte, 1<<20), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c.recoveryLog("retry", "network_or_lease_unavailable", 1)
	files, _ := os.ReadDir(c.dir)
	if len(files) != 3 {
		t.Fatal("unbounded diagnostic files")
	}
	for _, file := range files {
		info, _ := file.Info()
		if info.Size() > 1<<20 {
			t.Fatal("oversize diagnostic file")
		}
	}
	data, _ := os.ReadFile(filepath.Join(c.dir, "recovery.log"))
	if !strings.Contains(string(data), "network_or_lease_unavailable") {
		t.Fatal("missing safe diagnostic")
	}
}

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

const testSecret = "stats-secret-which-is-long-enough"
const firstPassword = "random-device-password-number-1"
const secondPassword = "random-device-password-number-2"

func testAgent(t *testing.T, statsURL, controlURL string) *Agent {
	t.Helper()
	if statsURL == "" {
		statsURL = "http://127.0.0.1:1"
	}
	if controlURL == "" {
		controlURL = "http://127.0.0.1:1"
	}
	a, err := New(model.AgentConfig{
		ControlURL: controlURL, Token: "agent-token-that-is-long-enough", NodeID: "node-a",
		Listen: "127.0.0.1:0", StatePath: filepath.Join(t.TempDir(), "state.db"),
		StatsURL: statsURL, StatsSecret: testSecret,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.generation = func(context.Context) (string, error) { return "core-a", nil }
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func desired(revision int64, password string, enabled bool) model.AgentDesired {
	return model.AgentDesired{Revision: revision, Devices: []model.AgentDevice{{ID: "phone", Password: password, Enabled: enabled}}}
}

func auth(t *testing.T, a *Agent, password string) (bool, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"addr": "198.51.100.2:4433", "auth": password, "tx": 0})
	r := httptest.NewRequest(http.MethodPost, "/auth?secret="+url.QueryEscape(testSecret), bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("auth HTTP %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.OK, response.ID
}

func TestAuthCachesOnlyHashesAndWorksOffline(t *testing.T) {
	a := testAgent(t, "", "")
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	if ok, id := auth(t, a, firstPassword); !ok || id != "phone" {
		t.Fatal("valid credential denied")
	}
	if ok, _ := auth(t, a, secondPassword); ok {
		t.Fatal("wrong credential accepted")
	}
	for _, remote := range []string{"127.0.0.1:53221", "192.0.2.1:53221"} {
		request := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(`{"auth":"`+firstPassword+`"}`))
		request.RemoteAddr = remote
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, request)
		if w.Code != http.StatusForbidden {
			t.Fatal("unauthenticated local API accepted")
		}
	}
	cfg := a.cfg
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, []byte(firstPassword)) {
		t.Fatal("plaintext proxy password persisted")
	}
	restarted, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if ok, id := auth(t, restarted, firstPassword); !ok || id != "phone" {
		t.Fatal("cached auth unavailable without control plane")
	}
}

func TestRevocationRepeatsSingleConsumerKickUntilAllInstancesGone(t *testing.T) {
	var instances atomic.Int64
	instances.Store(3)
	var kicks atomic.Int64
	stats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != testSecret {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/online":
			_ = json.NewEncoder(w).Encode(map[string]int64{"phone": instances.Load()})
		case "/kick":
			var ids []string
			if err := json.NewDecoder(r.Body).Decode(&ids); err != nil || len(ids) != 1 || ids[0] != "phone" {
				t.Error("invalid kick body")
			}
			kicks.Add(1)
			instances.Add(-1)
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer stats.Close()
	a := testAgent(t, stats.URL, "")
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, shanghai)
	a.now = func() time.Time { return now }
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	if err := a.applyDesired(desired(2, secondPassword, true)); err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{firstPassword, secondPassword} {
		if ok, _ := auth(t, a, password); ok {
			t.Fatal("rotation accepted auth before existing instances drained")
		}
	}
	for n := 0; n < 3; n++ {
		if err := a.drainRevocations(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
		if a.cache.AppliedRevision != 1 {
			t.Fatal("revision acknowledged before all instances gone")
		}
	}
	if kicks.Load() != 3 {
		t.Fatalf("kick calls = %d", kicks.Load())
	}
	if err := a.drainRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := a.drainRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.cache.AppliedRevision != 2 {
		t.Fatal("drained revision not acknowledged")
	}
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("old password survived rotation")
	}
	if ok, _ := auth(t, a, secondPassword); !ok {
		t.Fatal("new password still denied after drain")
	}
	if err := a.applyDesired(desired(3, secondPassword, false)); err != nil {
		t.Fatal(err)
	}
	stats.Close()
	if err := a.drainRevocations(context.Background()); err == nil {
		t.Fatal("missing stats server not detected")
	}
	if a.cache.AppliedRevision != 2 {
		t.Fatal("offline revoke incorrectly acknowledged")
	}
	if ok, _ := auth(t, a, secondPassword); ok {
		t.Fatal("offline revoke failed to block new auth")
	}
}

func TestReportRetriesAreCumulativeAndFailedStatsStayUnknown(t *testing.T) {
	var traffic atomic.Int64
	traffic.Store(100)
	var onlineFail atomic.Bool
	stats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != testSecret {
			w.WriteHeader(403)
			return
		}
		if r.URL.RawQuery != "" {
			t.Error("statistics must never use clear=1")
		}
		switch r.URL.Path {
		case "/traffic":
			_ = json.NewEncoder(w).Encode(map[string]counter{"phone": {traffic.Load(), traffic.Load() * 2}})
		case "/online":
			if onlineFail.Load() {
				w.WriteHeader(503)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"phone": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	defer stats.Close()
	var reports []model.AgentReport
	var reportsMu sync.Mutex
	var acceptResponse atomic.Bool
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token-that-is-long-enough" {
			t.Error("missing agent bearer")
			w.WriteHeader(403)
			return
		}
		if r.Method == http.MethodPost {
			var report model.AgentReport
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Error(err)
			}
			reportsMu.Lock()
			reports = append(reports, report)
			reportsMu.Unlock()
			// Simulate an accepted report whose acknowledgment was lost.
			if !acceptResponse.Load() {
				w.WriteHeader(503)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(desired(1, firstPassword, true))
	}))
	defer control.Close()
	a := testAgent(t, stats.URL, control.URL)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, shanghai)
	a.now = func() time.Time { return now }
	if err := a.sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.sync(context.Background()); err == nil {
		t.Fatal("failed report response ignored")
	}
	traffic.Store(150)
	now = now.Add(6 * time.Second)
	if err := a.sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	acceptResponse.Store(true)
	if err := a.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	reportsMu.Lock()
	snapshot := append([]model.AgentReport(nil), reports...)
	reportsMu.Unlock()
	if len(snapshot) != 2 {
		t.Fatalf("reports = %d", len(snapshot))
	}
	assertUsage(t, snapshot[0].Daily, 100, 200)
	assertUsage(t, snapshot[1].Daily, 150, 300)
	if snapshot[1].Online["phone"] != 2 || !snapshot[1].CoreHealthy {
		t.Fatal("online instances lost")
	}
	remaining, err := a.ledger.batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.daily)+len(remaining.minutes) != 0 {
		t.Fatal("successful reports not acknowledged")
	}
	onlineFail.Store(true)
	traffic.Store(200)
	now = now.Add(time.Second)
	if err := a.sample(context.Background()); err == nil {
		t.Fatal("online stats failure not detected")
	}
	if err := a.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	reportsMu.Lock()
	last := reports[len(reports)-1]
	reportsMu.Unlock()
	if last.CoreHealthy || last.Online != nil {
		t.Fatalf("failed stats misrepresented as known online counts: %+v", last)
	}
	assertUsage(t, last.Daily, 200, 400)
}

func TestSlowControlPlaneDoesNotBlockSampling(t *testing.T) {
	var samples atomic.Int64
	stats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/traffic":
			_ = json.NewEncoder(w).Encode(map[string]counter{"phone": {samples.Add(1) * 100, 0}})
		case "/online":
			_ = json.NewEncoder(w).Encode(map[string]int{})
		}
	}))
	defer stats.Close()
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
		}
		_ = json.NewEncoder(w).Encode(desired(1, firstPassword, true))
	}))
	defer control.Close()
	a := testAgent(t, stats.URL, control.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3200*time.Millisecond)
	defer cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if samples.Load() < 3 {
		t.Fatalf("slow control blocked sampling: %d samples", samples.Load())
	}
}

func TestCoreRestartDuringFetchDoesNotCommitMixedCounters(t *testing.T) {
	stats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/traffic" {
			_ = json.NewEncoder(w).Encode(map[string]counter{"phone": {100, 200}})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]int{})
		}
	}))
	defer stats.Close()
	a := testAgent(t, stats.URL, "")
	var calls int
	a.generation = func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "old", nil
		}
		return "new", nil
	}
	if err := a.sample(context.Background()); err == nil {
		t.Fatal("core restart during sampling not rejected")
	}
	b, err := a.ledger.batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.daily)+len(b.minutes) != 0 {
		t.Fatal("mixed-generation sample committed")
	}
}

func TestRestoreRevisionJumpImmediatelyReplaysAcknowledgedUsage(t *testing.T) {
	a := testAgent(t, "", "")
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.ledger.record(context.Background(), time.Now(), "core", map[string]counter{"phone": {100, 200}}); err != nil {
		t.Fatal(err)
	}
	b, err := a.ledger.batch()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ledger.acknowledge(b); err != nil {
		t.Fatal(err)
	}
	if err := a.applyDesired(desired(time.Now().UnixMilli(), firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	replay, err := a.ledger.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, replay.daily, 100, 200)
	assertUsage(t, replay.minutes, 100, 200)
}

func TestConcurrentAuthAndRotation(t *testing.T) {
	a := testAgent(t, "", "")
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				auth(t, a, firstPassword)
			}
		}()
	}
	if err := a.applyDesired(desired(2, secondPassword, true)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("rotated old credential accepted")
	}
}

func TestConfigRejectsNonLoopbackAndPlaintextRemoteControl(t *testing.T) {
	a := testAgent(t, "", "")
	for _, mutate := range []func(*model.AgentConfig){
		func(c *model.AgentConfig) { c.Listen = "0.0.0.0:18081" },
		func(c *model.AgentConfig) { c.StatsURL = "http://192.0.2.1:18082" },
		func(c *model.AgentConfig) { c.ControlURL = "http://example.com" },
		func(c *model.AgentConfig) { c.ControlURL = "https://user:password@example.com" },
		func(c *model.AgentConfig) { c.HysteriaService = "--all" },
	} {
		cfg := a.cfg
		mutate(&cfg)
		if err := normalizeConfig(&cfg); err == nil {
			t.Fatalf("unsafe config accepted: %+v", cfg)
		}
	}
}

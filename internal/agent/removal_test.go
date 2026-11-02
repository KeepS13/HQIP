package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func removeDesired(revision int64, enabled bool) model.AgentDesired {
	d := desired(revision, firstPassword, enabled)
	d.Devices[0].RemovalID = "removal-1"
	d.Devices[0].RevokeAfterSeconds = 60
	return d
}

func TestRemovalDeadlineSurvivesReplayRestartAndControlOutage(t *testing.T) {
	stats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(map[string]int{}) }))
	defer stats.Close()
	a := testAgent(t, stats.URL, "")
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	if err := a.applyDesired(removeDesired(2, true)); err != nil {
		t.Fatal(err)
	}
	wantDeadline := now.Add(time.Minute)
	if a.cache.Removals["removal-1"].Deadline != wantDeadline {
		t.Fatal("wrong initial deadline")
	}
	now = now.Add(30 * time.Second)
	if err := a.applyDesired(removeDesired(3, true)); err != nil {
		t.Fatal(err)
	}
	if !a.cache.Removals["removal-1"].Deadline.Equal(wantDeadline) {
		t.Fatal("repeat extended deadline")
	}
	if a.cache.AppliedRevision != 1 {
		t.Fatal("scheduled removal acknowledged as completed")
	}
	if ok, _ := auth(t, a, firstPassword); !ok {
		t.Fatal("grace period incorrectly shortened")
	}
	cfg := a.cfg
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.now = func() time.Time { return now }
	now = wantDeadline
	// The auth handler enforces the deadline before any timer or controller request.
	if ok, _ := auth(t, restarted, firstPassword); ok {
		t.Fatal("expired credential allowed after offline restart")
	}
	if err := restarted.drainRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)
	if err := restarted.drainRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !restarted.cache.Removals["removal-1"].Revoked || restarted.cache.AppliedRevision != 3 {
		t.Fatal("drain did not acknowledge removal")
	}
	if err := restarted.applyDesired(removeDesired(4, true)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth(t, restarted, firstPassword); ok {
		t.Fatal("replayed command resurrected expired credential")
	}
	if err := restarted.applyDesired(desired(5, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth(t, restarted, firstPassword); ok {
		t.Fatal("omitting command resurrected tombstoned credential")
	}
	if err := restarted.applyDesired(desired(6, secondPassword, true)); err != nil {
		t.Fatal(err)
	}
	if err := restarted.drainRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)
	if err := restarted.drainRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth(t, restarted, secondPassword); !ok {
		t.Fatal("fresh authorized credential was not accepted")
	}
}

func TestImmediateRemovalCannotBeUndoneByLaterGraceCommand(t *testing.T) {
	a := testAgent(t, "", "")
	now := time.Now().UTC()
	a.now = func() time.Time { return now }
	if err := a.applyDesired(removeDesired(1, true)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	if err := a.applyDesired(removeDesired(2, false)); err != nil {
		t.Fatal(err)
	}
	deadline := a.cache.Removals["removal-1"].Deadline
	if !deadline.Equal(now) {
		t.Fatal("immediate completion failed to shorten deadline")
	}
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("immediate removal allowed auth")
	}
	now = now.Add(time.Second)
	if err := a.applyDesired(removeDesired(3, true)); err != nil {
		t.Fatal(err)
	}
	if !a.cache.Removals["removal-1"].Deadline.Equal(deadline) {
		t.Fatal("deadline extended after immediate revoke")
	}
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("later grace resurrected credential")
	}
	conflict := removeDesired(4, true)
	conflict.Devices[0].Password = secondPassword
	if err := a.applyDesired(conflict); err == nil {
		t.Fatal("same removal ID accepted for different credential")
	}
}

func TestOrdinaryAuthorizationHasNoLease(t *testing.T) {
	a := testAgent(t, "", "")
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return time.Now().AddDate(1, 0, 0) }
	if ok, _ := auth(t, a, firstPassword); !ok {
		t.Fatal("ordinary authorization expired while controller offline")
	}
}

func TestStuckRevocationRestartsCoreWithoutClearingUsage(t *testing.T) {
	stats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/online":
			_ = json.NewEncoder(w).Encode(map[string]int{"phone": 2})
		case "/traffic":
			_ = json.NewEncoder(w).Encode(map[string]counter{"phone": {100, 200}})
		case "/kick":
			w.WriteHeader(204)
		}
	}))
	defer stats.Close()
	a := testAgent(t, stats.URL, "")
	now := time.Now().UTC()
	a.now = func() time.Time { return now }
	generation := "old"
	a.generation = func(context.Context) (string, error) { return generation, nil }
	restarts := 0
	a.restartCore = func(context.Context) error { restarts++; generation = "new"; return nil }
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	if err := a.sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.applyDesired(removeDesired(2, false)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Second)
	if err := a.drainRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || a.cache.Warning == "" || !a.cache.Removals["removal-1"].Revoked {
		t.Fatal("forced drain not confirmed and alerted")
	}
	batch, err := a.ledger.batch()
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, batch.daily, 100, 200)
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("restart lost denied credential")
	}
}

func TestRestartWithoutNewGenerationDoesNotAcknowledgeRevocation(t *testing.T) {
	stats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer stats.Close()
	a := testAgent(t, stats.URL, "")
	now := time.Now().UTC()
	a.now = func() time.Time { return now }
	a.generation = func(context.Context) (string, error) { return "unchanged", nil }
	restarts := 0
	a.restartCore = func(context.Context) error { restarts++; return nil }
	if err := a.applyDesired(removeDesired(1, false)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Second)
	if err := a.drainRevocations(context.Background()); err == nil {
		t.Fatal("unconfirmed restart silently accepted")
	}
	if err := a.drainRevocations(context.Background()); err == nil {
		t.Fatal("throttled restart silently accepted")
	}
	if restarts != 1 || a.cache.Removals["removal-1"].Revoked {
		t.Fatal("false acknowledgement or restart storm")
	}
}

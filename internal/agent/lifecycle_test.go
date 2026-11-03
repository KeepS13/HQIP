package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestRetirementDeniesPersistentlyAndFinalizesOnlyAfterReportAck(t *testing.T) {
	a := testAgent(t, "", "")
	a.cfg.Managed = true
	a.prepareRetirement = func(context.Context) error { return nil }
	if err := a.applyDesired(desired(1, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	stopCalls, disableCalls, finishCalls := 0, 0, 0
	a.stopCore = func(context.Context) error { stopCalls++; return nil }
	a.disableAgent = func(context.Context) error { disableCalls++; return nil }
	a.finishRetirement = func(context.Context) error { finishCalls++; return nil }
	d := model.AgentDesired{Revision: 1, Retirement: &model.AgentRetirement{ID: "retire-a"}}
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	} // identical revision still applies lifecycle commands
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("retiring node still grants authentication")
	}
	cached, err := a.ledger.loadCache()
	if err != nil || cached.Retirement == nil {
		t.Fatal("retirement was not durable")
	}
	if err = a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stopCalls != 1 || disableCalls != 0 || finishCalls != 0 {
		t.Fatal("retirement skipped acknowledgement phase")
	}
	d.Retirement.Finalize = true
	if err = a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	if err = a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stopCalls != 1 || disableCalls != 1 || finishCalls != 0 {
		t.Fatal("finalization stopped Agent before report ack")
	}
	if !a.cache.Retirement.Final || len(a.cache.Credentials) != 0 {
		t.Fatal("finalization retained active grants")
	}
	if err = a.acknowledgeRetirement(context.Background(), &model.RetirementStatus{ID: "retire-a", Stopped: true, Final: true}); err != nil {
		t.Fatal(err)
	}
	if finishCalls != 1 {
		t.Fatal("final ack did not cleanup")
	}
	if err = a.applyDesired(desired(2, firstPassword, true)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("later stale grant resurrected retired node")
	}
}

func TestRetirementStopFailureCannotReportCompletion(t *testing.T) {
	a := testAgent(t, "", "")
	a.cfg.Managed = true
	a.prepareRetirement = func(context.Context) error { return nil }
	a.stopCore = func(context.Context) error { return errors.New("stop failed") }
	if err := a.applyDesired(model.AgentDesired{Revision: 1, Retirement: &model.AgentRetirement{ID: "retire-b", Finalize: true}}); err != nil {
		t.Fatal(err)
	}
	if err := a.progressRetirement(context.Background()); err == nil {
		t.Fatal("stop failure swallowed")
	}
	if a.cache.Retirement.Stopped || a.cache.Retirement.Final {
		t.Fatal("failed stop claimed completion")
	}
	if err := a.applyDesired(model.AgentDesired{Revision: 2, Retirement: &model.AgentRetirement{ID: "another-id"}}); err == nil {
		t.Fatal("replacement retirement ID accepted")
	}
}

func TestRetirementLostHTTPAckDoesNotCleanupAgent(t *testing.T) {
	ack := false
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !ack {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(model.AgentDesired{Revision: 1, Retirement: &model.AgentRetirement{ID: "retire-c", Finalize: true}})
	}))
	defer control.Close()
	a := testAgent(t, "", control.URL)
	a.cfg.Managed = true
	a.prepareRetirement = func(context.Context) error { return nil }
	a.stopCore = func(context.Context) error { return nil }
	a.disableAgent = func(context.Context) error { return nil }
	finished := 0
	a.finishRetirement = func(context.Context) error { finished++; return nil }
	if err := a.sync(context.Background()); err == nil {
		t.Fatal("report failure swallowed")
	}
	if finished != 0 {
		t.Fatal("Agent cleaned up before acknowledgement")
	}
	ack = true
	if err := a.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if finished != 1 {
		t.Fatal("retry did not finalize after ack")
	}
}

func TestUnmanagedConfigDoesNotAllowRemoteRetirement(t *testing.T) {
	a := testAgent(t, "", "")
	if err := a.applyDesired(model.AgentDesired{Revision: 1, Retirement: &model.AgentRetirement{ID: "retire"}}); err == nil {
		t.Fatal("legacy unmanaged service accepted privileged retirement")
	}
	if a.cache.Retirement != nil {
		t.Fatal("unmanaged config mutated")
	}
}

func TestRetirementCleanupFailureKeepsFinalPending(t *testing.T) {
	a := testAgent(t, "", "")
	a.cfg.Managed = true
	a.stopCore = func(context.Context) error { return nil }
	a.disableAgent = func(context.Context) error { return nil }
	a.prepareRetirement = func(context.Context) error { return errors.New("archive unavailable") }
	d := model.AgentDesired{Revision: 1, Retirement: &model.AgentRetirement{ID: "retire-cleanup", Finalize: true}}
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	if err := a.progressRetirement(context.Background()); err == nil {
		t.Fatal("cleanup error hidden")
	}
	if !a.cache.Retirement.Stopped || a.cache.Retirement.Final {
		t.Fatal("cleanup failure reported final completion")
	}
	a.prepareRetirement = func(context.Context) error { return nil }
	if err := a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.cache.Retirement.Final {
		t.Fatal("cleanup retry did not finalize")
	}
}

func TestRetirementWaitsForAllOfflineTrafficAcknowledgments(t *testing.T) {
	a := testAgent(t, "", "")
	a.cfg.Managed = true
	a.stopCore = func(context.Context) error { return nil }
	a.disableAgent = func(context.Context) error { return nil }
	prepared := 0
	a.prepareRetirement = func(context.Context) error { prepared++; return nil }
	if _, _, err := a.ledger.record(context.Background(), a.now(), "generation", map[string]counter{"phone": {TX: 100, RX: 200}}); err != nil {
		t.Fatal(err)
	}
	if err := a.applyDesired(model.AgentDesired{Revision: 1, Retirement: &model.AgentRetirement{ID: "retire-offline", Finalize: true}}); err != nil {
		t.Fatal(err)
	}
	if err := a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prepared != 0 || a.cache.Retirement.Final {
		t.Fatal("retirement discarded unacknowledged offline usage")
	}
	batch, err := a.ledger.batch()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ledger.acknowledge(batch); err != nil {
		t.Fatal(err)
	}
	if err = a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prepared != 1 || !a.cache.Retirement.Final {
		t.Fatal("acknowledged offline usage blocked completion")
	}
}

func TestPrivateArchiveWorksAcrossServiceMounts(t *testing.T) {
	from := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(from, []byte("private-config"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if runtime.GOOS == "linux" {
		if dir, err := os.MkdirTemp("/dev/shm", "hqip-archive-test-"); err == nil {
			dest = dir
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
		}
	}
	to := filepath.Join(dest, "agent.json")
	if err := archivePrivateFile(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatal("active config was not removed after durable archive")
	}
	data, err := os.ReadFile(to)
	if err != nil || string(data) != "private-config" {
		t.Fatal("archive changed")
	}
}

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestRetirementWaitsForDurableFinalAndLateArrival(t *testing.T) {
	online := false
	confirmed := false
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online {
			w.WriteHeader(503)
			return
		}
		var usage model.LeaseUsage
		_ = json.NewDecoder(r.Body).Decode(&usage)
		if !usage.Closed {
			t.Error("retirement renewed unfinished lease")
		}
		confirmed = true
		_ = json.NewEncoder(w).Encode(map[string]any{"leaseId": "retire-lease", "uploadBytes": usage.UploadBytes, "downloadBytes": usage.DownloadBytes, "closed": true})
	}))
	defer control.Close()
	a := testAgent(t, "", control.URL)
	a.cfg.Managed = true
	now := time.Now()
	a.now = func() time.Time { return now }
	disabled, finished := 0, 0
	a.stopCore = func(context.Context) error { return nil }
	a.disableAgent = func(context.Context) error { disabled++; return nil }
	a.prepareRetirement = func(context.Context) error { return nil }
	a.finishRetirement = func(context.Context) error { finished++; return nil }
	_, err := a.ledger.saveLeaseUsage("retire-lease", model.LeaseUsage{UploadBytes: 100}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.applyDesired(model.AgentDesired{Revision: 1, PortalEnabled: true, Retirement: &model.AgentRetirement{ID: "retire-queue", Finalize: true}}); err != nil {
		t.Fatal(err)
	}
	if err = a.progressRetirement(context.Background()); err == nil {
		t.Fatal("empty dirty-final queue falsely proved final has arrived")
	}
	if disabled != 0 || a.cache.Retirement.Final {
		t.Fatal("Agent disabled before final arrival window")
	}
	if _, err = a.ledger.saveLeaseUsage("retire-lease", model.LeaseUsage{UploadBytes: 200, DownloadBytes: 300, Closed: true}, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	if err = a.progressRetirement(context.Background()); err == nil {
		t.Fatal("offline queued final did not block finalization")
	}
	if disabled != 0 {
		t.Fatal("Agent disabled with unconfirmed final")
	}
	online = true
	now = now.Add(6 * time.Second)
	if err = a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !confirmed || disabled != 1 || !a.cache.Retirement.Final {
		t.Fatal("final report was not confirmed before Agent disable")
	}
	// A final arriving after the earlier completion check still blocks the final
	// service-stop acknowledgment until its own response has been confirmed.
	_, err = a.ledger.saveLeaseUsage("retire-lease", model.LeaseUsage{UploadBytes: 200, DownloadBytes: 300, Closed: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	online = false
	report := &model.RetirementStatus{ID: "retire-queue", Stopped: true, Final: true}
	if err = a.acknowledgeRetirement(context.Background(), report); err == nil || finished != 0 {
		t.Fatal("late queued final was lost during stop acknowledgement")
	}
	online = true
	now = now.Add(6 * time.Second)
	if err = a.acknowledgeRetirement(context.Background(), report); err != nil || finished != 1 {
		t.Fatal("confirmed final did not allow stop", err)
	}
}

func TestRetirementMissingCrashFinalPreservesReservationWithoutBlockingDelete(t *testing.T) {
	a := testAgent(t, "", "")
	a.cfg.Managed = true
	now := time.Now()
	a.now = func() time.Time { return now }
	a.stopCore = func(context.Context) error { return nil }
	a.disableAgent = func(context.Context) error { return nil }
	a.prepareRetirement = func(context.Context) error { return nil }
	_, err := a.ledger.saveLeaseUsage("crashed-no-final", model.LeaseUsage{UploadBytes: 100}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.applyDesired(model.AgentDesired{Revision: 1, PortalEnabled: true, Retirement: &model.AgentRetirement{ID: "retire-crash", Finalize: true}}); err != nil {
		t.Fatal(err)
	}
	_ = a.progressRetirement(context.Background())
	now = now.Add(6 * time.Second)
	if err = a.progressRetirement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.cache.Retirement.Final || a.cache.Warning == "" {
		t.Fatal("missing final either blocks deletion forever or is silently treated as settled")
	}
	var closed int
	_ = a.ledger.db.QueryRow("SELECT closed FROM lease_usage WHERE lease_id='crashed-no-final'").Scan(&closed)
	if closed != 0 {
		t.Fatal("crash invented a final report")
	}
}

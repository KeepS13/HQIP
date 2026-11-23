package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestAgentReportAlreadyDeliversDesiredConfiguration(t *testing.T) {
	var configs, reports atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agents/report" {
			reports.Add(1)
		} else {
			configs.Add(1)
		}
		_ = json.NewEncoder(w).Encode(model.AgentDesired{Revision: int64(reports.Load()) + 1})
	}))
	defer control.Close()
	a := testAgent(t, "", control.URL)
	for range 2 {
		if err := a.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if configs.Load() != 0 || reports.Load() != 2 {
		t.Fatalf("healthy reports redundantly fetched configuration: configs=%d reports=%d", configs.Load(), reports.Load())
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if a.cache.DesiredRevision != 3 {
		t.Fatal("report configuration was not applied", a.cache.DesiredRevision)
	}
}

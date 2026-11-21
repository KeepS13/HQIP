package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestAdmissionSlowSuccessfulControlResponseIsDelivered(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"leaseId":"slow-grant"}`))
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	if err := a.applyDesired(model.AgentDesired{Revision: 1, PortalEnabled: true}); err != nil {
		t.Fatal(err)
	}
	local := httptest.NewUnstartedServer(a.Handler())
	local.Config.WriteTimeout = admissionWriteTimeout
	local.Start()
	defer local.Close()
	body := `{"clientIP":"198.51.100.1","billingCapability":"traffic-billing-v1","connectionId":"` + strings.Repeat("a", 64) + `","password":"` + firstPassword + `"}`
	req, _ := http.NewRequest(http.MethodPost, local.URL+"/admission", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testSecret)
	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("successful grant was dropped", err)
	}
	defer response.Body.Close()
	var grant struct {
		LeaseID string `json:"leaseId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&grant); err != nil || response.StatusCode != 200 || grant.LeaseID != "slow-grant" {
		t.Fatalf("invalid delivered grant: status=%d grant=%+v err=%v", response.StatusCode, grant, err)
	}
}

type failedAdmissionWriter struct{ *httptest.ResponseRecorder }

func (w failedAdmissionWriter) FlushError() error { return errors.New("connection closed") }

func TestUndeliveredAdmissionFinalSurvivesRestart(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "flush-failure", true: "caller-cancelled"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var finals atomic.Int32
			central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/renew") {
					var usage model.LeaseUsage
					if err := json.NewDecoder(r.Body).Decode(&usage); err != nil || !usage.Closed || usage.UploadBytes != 0 || usage.DownloadBytes != 0 {
						t.Error("invalid undelivered final", err)
					}
					finals.Add(1)
					_, _ = w.Write([]byte(`{"leaseId":"undelivered","closed":true,"uploadBytes":0,"downloadBytes":0}`))
					return
				}
				if cancelled {
					cancel()
				}
				_, _ = w.Write([]byte(`{"leaseId":"undelivered"}`))
			}))
			defer central.Close()
			a := testAgent(t, "", central.URL)
			req := httptest.NewRequest("POST", "/admission", nil).WithContext(ctx)
			a.forwardAdmission(failedAdmissionWriter{httptest.NewRecorder()}, req, "/api/v1/agents/admission", struct{}{})
			pending, err := a.ledger.pendingLeaseFinals(a.now())
			if err != nil || len(pending) != 1 || pending[0].ID != "undelivered" {
				t.Fatalf("failed grant was not queued: %+v %v", pending, err)
			}
			cfg := a.cfg
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if err = restored.replayLeaseFinals(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err = restored.ledger.leaseCapacity(restored.now().Add(48 * time.Hour)); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = restored.ledger.db.QueryRow("SELECT COUNT(*) FROM lease_usage").Scan(&count); err != nil || count != 0 || finals.Load() != 1 {
				t.Fatalf("undelivered row retained: count=%d finals=%d err=%v", count, finals.Load(), err)
			}
		})
	}
}

func TestAdmissionBusyOrCancelledNeverRequestsAnotherGrant(t *testing.T) {
	var calls atomic.Int32
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.admissionMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.forwardAdmission(httptest.NewRecorder(), httptest.NewRequest("POST", "/admission", nil).WithContext(ctx), "/api/v1/agents/admission", struct{}{})
		close(done)
	}()
	cancel()
	a.admissionMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled admission remained queued")
	}
	a.forwardAdmission(httptest.NewRecorder(), httptest.NewRequest("POST", "/admission", nil).WithContext(ctx), "/api/v1/agents/admission", struct{}{})
	if calls.Load() != 0 {
		t.Fatal("busy or cancelled request allocated a grant")
	}
}

func TestDuplicateUndeliveredAdmissionDoesNotCloseExistingGrant(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"leaseId":"existing-grant"}`))
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	for _, used := range []int64{0, 100} {
		pending, err := a.ledger.saveLeaseUsage("existing-grant", model.LeaseUsage{UploadBytes: used}, a.now())
		if err != nil {
			t.Fatal(err)
		}
		if err = a.ledger.ackLeaseUsage(pending); err != nil {
			t.Fatal(err)
		}
		a.forwardAdmission(failedAdmissionWriter{httptest.NewRecorder()}, httptest.NewRequest("POST", "/admission", nil), "/api/v1/agents/admission", struct{}{})
		var upload int64
		var closed bool
		if err = a.ledger.db.QueryRow("SELECT upload,closed FROM lease_usage WHERE lease_id=?", "existing-grant").Scan(&upload, &closed); err != nil || closed || upload != used {
			t.Fatalf("duplicate delivery failure changed live usage: upload=%d closed=%t err=%v", upload, closed, err)
		}
	}
}

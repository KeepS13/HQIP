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

// A malformed successful reply is also an unknown outcome. A rejection on a
// later attempt cannot erase the earlier committed admission's recovery key.
func TestOfflineRequestFailurePreservesOnlyUncertainAdmissions(t *testing.T) {
	for _, scenario := range []string{"truncated_reply", "rejected_retry", "first_rejection"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := calls.Add(1)
				if scenario == "truncated_reply" {
					_, _ = w.Write([]byte(`{"id":`))
				} else if scenario == "first_rejection" || attempt > 1 {
					w.WriteHeader(403)
				} else {
					w.WriteHeader(503)
				}
			}))
			defer central.Close()
			a := testAgent(t, "", central.URL)
			a.cache.PortalEnabled, a.cache.OfflineAdmissionVersion = true, 1
			attempts := 1
			if scenario == "rejected_retry" {
				attempts = 2
			}
			for i := 0; i < attempts; i++ {
				w := admissionRequest(a, "/admission", `{"clientIP":"198.51.100.1","billingCapability":"traffic-billing-v1","connectionId":"`+strings.Repeat(string(rune('a'+i)), 64)+`","password":"`+firstPassword+`"}`, "127.0.0.1:3000", testSecret)
				if w.Code == 200 {
					t.Fatal("failed admission was delivered")
				}
			}
			pending, err := a.ledger.offlineRequests("")
			want := 1
			if scenario == "first_rejection" {
				want = 0
			}
			if err != nil || len(pending) != want {
				t.Fatal("uncertain outcome lost, or definitive rejection retained", pending, err)
			}
		})
	}
}

func TestOfflineRecoveryTimeoutStillAllowsKnownSlotRelease(t *testing.T) {
	now := time.Now()
	s := quotaTestState(now, "known-idle", "hysteria2", 100_000_000)
	s.Grant.ClientIP, s.Grant.ConnectionSlotsVersion, s.Online = "198.51.100.1", model.ConnectionSlotsVersion, true
	a := testAgent(t, "", "")
	a.now = func() time.Time { return now }
	a.cache.PortalEnabled, a.cache.NodeQuotaVersion = true, 1
	if err := a.ledger.saveOffline(s); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.ledger.trackOfflineRequest("hysteria2:unknown:198.51.100.2", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	g := s.Grant
	// Exercise the production budget. A 300 ms sweep leaves only 150 ms for
	// durable SQLite reconciliation and slot release on a loaded Windows runner.
	// Assert the nested deadline explicitly instead of relying on a tiny clock.
	ctx, cancel := context.WithTimeout(context.Background(), admissionControlTimeout)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	recoveryTimedOut := false
	a.controlClient.Transport = offlineRecoveryTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/recover") {
			deadline, bounded := r.Context().Deadline()
			if !bounded || parentDeadline.Sub(deadline) < admissionControlTimeout/3 {
				t.Fatal("recovery failed to reserve the normal-report budget")
			}
			<-r.Context().Done()
			recoveryTimedOut = errors.Is(r.Context().Err(), context.DeadlineExceeded)
			return nil, r.Context().Err()
		}
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		var u model.OfflineUsage
		_ = json.NewDecoder(r.Body).Decode(&u)
		g.ReportVersion, g.SlotReleased = u.Version, u.SlotAction == "release"
		w := httptest.NewRecorder()
		_ = json.NewEncoder(w).Encode(g)
		return w.Result(), nil
	})
	err := a.syncOfflineGrants(ctx)
	if !recoveryTimedOut || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("recovery timeout was not exercised", err)
	}
	if !g.SlotReleased {
		t.Fatal("unavailable recovery blocked a known idle slot's release")
	}
}

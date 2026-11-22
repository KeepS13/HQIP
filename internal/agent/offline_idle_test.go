package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

type idleReportRecorder struct {
	mu     sync.Mutex
	grant  model.OfflineGrant
	bodies []string
	status int
}

func idleReportFixture(t *testing.T, s offlineState) (*Agent, *idleReportRecorder) {
	t.Helper()
	r := &idleReportRecorder{grant: s.Grant, status: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		data, _ := io.ReadAll(req.Body)
		r.bodies = append(r.bodies, string(data))
		var u model.OfflineUsage
		if json.Unmarshal(data, &u) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g := r.grant
		g.ReportVersion, g.UploadBytes, g.DownloadBytes = u.Version, u.UploadBytes, u.DownloadBytes
		g.EmergencyUsedBytes, g.Closed = u.EmergencyUsedBytes, u.Closed
		if u.SlotAction != "" {
			g.SlotReleased = u.SlotAction == "release"
		}
		if u.RetainedNormalUnits != nil {
			g.Revoked, g.NormalBudgetBytes = true, (*u.RetainedNormalUnits+99)/100
		}
		if g.Billing != nil {
			copy := *g.Billing
			copy.Acknowledged = append([]billing.Segment(nil), u.Billing...)
			g.Billing = &copy
		}
		r.grant = g // A failed reply may follow an already-committed report.
		if r.status != http.StatusOK {
			w.WriteHeader(r.status)
			return
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	t.Cleanup(server.Close)
	return testAgent(t, "", server.URL), r
}

func (r *idleReportRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func TestIdleOfflineReportsSleepAcrossRestartAndWakeForLateUsage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	applied := now
	s := quotaTestState(now, "sleeping-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion, s.Grant.SlotReleased = model.ConnectionSlotsVersion, true
	s.Children["unknown"] = offlineChild{ID: "unknown", Ended: true, Normal: 100_000, Billing: map[int64]billing.Segment{}}
	a, reports := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	if err := a.syncOfflineState(context.Background(), &s, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		now = now.Add(6 * time.Second)
		if err := a.syncOfflineState(context.Background(), &s, false); err != nil {
			t.Fatal(err)
		}
	}
	if reports.count() != 1 || s.NextVersion != 1 || s.Grant.Closed || s.Children["unknown"].Normal != 100_000 {
		t.Fatal("idle polling sent reports or discarded uncertain usage", reports.count(), s)
	}
	path := a.cfg.StatePath
	_ = a.ledger.close()
	var err error
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	states, err := a.ledger.offlineStates()
	if err != nil || len(states) != 1 {
		t.Fatal(states, err)
	}
	s = states[0]
	if err = a.syncOfflineState(context.Background(), &s, false); err != nil || reports.count() != 1 {
		t.Fatal("restart lost acknowledgement", err, reports.count())
	}
	final := model.LeaseUsage{DownloadBytes: 100, Closed: true, Billing: []billing.Segment{{Version: 1, Download: 100, AppliedAt: applied}}}
	if err = s.updateChild("unknown", final, now); err != nil {
		t.Fatal(err)
	}
	if err = a.syncOfflineState(context.Background(), &s, false); err != nil || reports.count() != 2 || s.Grant.DownloadBytes != 100 {
		t.Fatal("sleep swallowed late usage", err, reports.count(), s.Grant)
	}
	now = now.Add(6 * time.Second)
	if err = a.syncOfflineState(context.Background(), &s, false); err != nil || reports.count() != 2 {
		t.Fatal("acknowledged billing caused repeated reports", err, reports.count())
	}
	if err = a.prepareOfflineSlot(context.Background(), &s); err != nil || s.Grant.SlotReleased || reports.count() != 4 {
		t.Fatal("reconnect skipped control validation and slot acquisition", err, reports.count())
	}
}

func TestIdleOfflineReportsReplayLostReplyBeforeSleeping(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "pending-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion, s.Grant.SlotReleased = model.ConnectionSlotsVersion, true
	s.Children["unknown"] = offlineChild{ID: "unknown", Ended: true, Normal: 100_000}
	a, reports := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	reports.status = http.StatusServiceUnavailable
	if err := a.syncOfflineState(context.Background(), &s, false); err != nil || s.Pending == nil || s.AcknowledgedReport != "" {
		t.Fatal("lost acknowledgement treated as complete", err, s)
	}
	path := a.cfg.StatePath
	_ = a.ledger.close()
	var err error
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	states, _ := a.ledger.offlineStates()
	s = states[0]
	now = now.Add(6 * time.Second)
	reports.mu.Lock()
	reports.status = http.StatusOK
	reports.mu.Unlock()
	if err = a.syncOfflineState(context.Background(), &s, false); err != nil || s.Pending != nil {
		t.Fatal("pending report was not acknowledged", err)
	}
	now = now.Add(6 * time.Second)
	if err = a.syncOfflineState(context.Background(), &s, false); err != nil {
		t.Fatal(err)
	}
	reports.mu.Lock()
	defer reports.mu.Unlock()
	if len(reports.bodies) != 2 || reports.bodies[0] != reports.bodies[1] {
		t.Fatal("pending replay changed or continued after acknowledgement", reports.bodies)
	}
}

func TestIdleOfflineReportsKeepHeldSlotsAndActiveConnectionsFresh(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "held-idle-slot", true: "active-connection"}[active], func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			s := quotaTestState(now, "held-parent", "hysteria2", 100_000_000)
			s.Grant.ConnectionSlotsVersion = model.ConnectionSlotsVersion
			if active {
				s.Children["live"] = offlineChild{ID: "live", Until: now.Add(time.Hour), Normal: 100_000, AppliedRate: 1}
			}
			a, reports := idleReportFixture(t, s)
			a.now = func() time.Time { return now }
			for i := 0; i < 3; i++ {
				if err := a.syncOfflineState(context.Background(), &s, false); err != nil {
					t.Fatal(err)
				}
				now = now.Add(6 * time.Second)
			}
			if reports.count() != 3 {
				t.Fatal("held authorization stopped checking control", reports.count())
			}
		})
	}
}

func TestIdleOfflineLegacyRetirementSleepsWithoutLosingUnknownBudget(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := offlineState{Grant: offlineTestGrant(now), LastWall: now, Children: map[string]offlineChild{
		"unknown": {ID: "unknown", Ended: true, Normal: 1000},
	}}
	a, reports := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	a.cache.NodeQuotaVersion = 1
	for i := 0; i < 60; i++ {
		if err := a.retireIdleLegacyGrant(context.Background(), &s); err != nil {
			t.Fatal(err)
		}
		if err := a.syncOfflineState(context.Background(), &s, false); err != nil {
			t.Fatal(err)
		}
		now = now.Add(6 * time.Second)
	}
	if reports.count() != 1 || s.Grant.Closed || !s.Grant.Revoked || s.Children["unknown"].Normal != 1000 {
		t.Fatal("legacy retirement kept polling or discarded delegated bytes", reports.count(), s)
	}
	if err := s.updateChild("unknown", model.LeaseUsage{DownloadBytes: 13, Closed: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := a.retireIdleLegacyGrant(context.Background(), &s); err != nil || !s.Grant.Closed || s.Grant.DownloadBytes != 13 || reports.count() != 2 {
		t.Fatal("sleep prevented late final settlement", err, reports.count(), s.Grant)
	}
}

func TestIdleOfflineReportsForcedReconnectObservesRevocation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "reconnect-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion, s.Grant.SlotReleased = model.ConnectionSlotsVersion, true
	a, reports := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	if err := a.syncOfflineState(context.Background(), &s, false); err != nil {
		t.Fatal(err)
	}
	reports.mu.Lock()
	reports.status = http.StatusForbidden
	reports.mu.Unlock()
	if err := a.prepareOfflineSlot(context.Background(), &s); err != errOfflineDenied || reports.count() != 2 || !s.Denied {
		t.Fatal("sleep reused cached permission after control denied reconnect", err, reports.count())
	}
}

func TestIdleOfflineReportsDoNotGuessTerminationFromWallClock(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "unproven-parent", "hysteria2", 100_000_000)
	s.Grant.Revoked = true
	s.Children["unproven"] = offlineChild{ID: "unproven", Normal: 100_000, Until: now.Add(-time.Hour)}
	a, reports := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if err := a.syncOfflineState(context.Background(), &s, false); err != nil {
			t.Fatal(err)
		}
		now = now.Add(6 * time.Second)
	}
	if reports.count() != 3 || s.Children["unproven"].Ended || s.Children["unproven"].Normal != 100_000 {
		t.Fatal("wall clock expiry fabricated termination or freed unknown usage", reports.count(), s)
	}
}

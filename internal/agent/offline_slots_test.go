package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func TestOfflineSlotFinalReleasesImmediatelyAndReacquires(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "slot-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion = 1
	s.Online = true
	s.RetryAt = now.Add(time.Hour) // Final must bypass ordinary sync backoff.
	var mu sync.Mutex
	g := s.Grant
	var actions []string
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var u model.OfflineUsage
		_ = json.NewDecoder(r.Body).Decode(&u)
		actions = append(actions, u.SlotAction)
		g.ReportVersion, g.DownloadBytes, g.UploadBytes = u.Version, u.DownloadBytes, u.UploadBytes
		if u.SlotAction != "" {
			g.SlotReleased = u.SlotAction == "release"
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	if _, err := a.localOfflineGrant(&s, "off_child", now); err != nil {
		t.Fatal(err)
	}
	if err := a.ledger.saveOffline(s); err != nil {
		t.Fatal(err)
	}
	u := model.LeaseUsage{Closed: true, DownloadBytes: 100, Billing: []billing.Segment{{Version: 1, Download: 100, AppliedAt: now}}}
	data, _ := json.Marshal(u)
	w := admissionRequest(a, "/leases/off_child/renew", string(data), "127.0.0.1:3000", testSecret)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, _, _ = a.ledger.offlineChild("off_child")
	if !s.Grant.SlotReleased || s.Grant.Closed || s.Children["off_child"].NormalUsed != 15000 {
		t.Fatal("final did not release slot or lost metering", s)
	}
	if _, err := a.localOfflineGrant(&s, "off_unsafe", now); err == nil {
		t.Fatal("released cached grant forwarded without website")
	}
	if err := a.prepareOfflineSlot(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.localOfflineGrant(&s, "off_next", now); err != nil {
		t.Fatal("reacquired slot unavailable", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(actions, []string{"", "release", "", "acquire"}) {
		t.Fatal("unexpected slot transitions", actions)
	}
}

func TestOfflineSlotLostReplyFencesReuseAndReplaysAfterRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "slot-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion, s.Online = 1, true
	var mu sync.Mutex
	var reports []model.OfflineUsage
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var u model.OfflineUsage
		_ = json.NewDecoder(r.Body).Decode(&u)
		reports = append(reports, u)
		if len(reports) == 1 {
			w.WriteHeader(503)
			return
		}
		g := s.Grant
		g.ReportVersion, g.SlotReleased = u.Version, true
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	if err := a.releaseIdleOfflineSlot(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.localOfflineGrant(&s, "unsafe", now); err == nil {
		t.Fatal("uncertain release reused slot")
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
	if err = a.syncOfflineState(context.Background(), &s, true); err != nil {
		t.Fatal(err)
	}
	if !s.Grant.SlotReleased || s.SlotAction != "" || s.Pending != nil {
		t.Fatal("release acknowledgement lost", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 2 || !reflect.DeepEqual(reports[0], reports[1]) {
		t.Fatal("restart mutated in-flight report", reports)
	}
}

func TestOfflineSlotTimeoutIsPerConnectionAndPreservesUnknownUsage(t *testing.T) {
	a := testAgent(t, "", "")
	now := a.now()
	s := quotaTestState(now, "slot-parent", "hysteria2", 100_000_000)
	s.Children["expired"] = offlineChild{ID: "expired", Normal: 100_000, NormalUsed: 1000, CoreGeneration: "same"}
	s.Children["active"] = offlineChild{ID: "active", Normal: 100_000, CoreGeneration: "same"}
	a.offlineIssued = map[string]time.Time{"expired": time.Now().Add(-126 * time.Second), "active": time.Now()}
	a.reconcileOfflineChildren(context.Background(), &s, "same")
	if !s.Children["expired"].Ended || s.Children["expired"].Closed || s.Children["expired"].Normal != 100_000 || s.Children["active"].Ended || idleOfflineSlot(&s) {
		t.Fatal("timeout lost accounting or ended live sibling", s)
	}
}

// Revocation must stop reuse, but must not retain an ended transport's slot.
// A late exact final can still settle the preserved unknown byte allocation.
func TestOfflineSlotRevokedEndedChildReleasesAndAcceptsLateFinal(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "slot-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion, s.Online = 1, true
	s.Children["ended"] = offlineChild{ID: "ended", Ended: true, Normal: 100_000, Billing: map[int64]billing.Segment{}}
	s.Denied, s.Grant.Revoked = true, true
	g := s.Grant
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u model.OfflineUsage
		_ = json.NewDecoder(r.Body).Decode(&u)
		ack := g
		ack.ReportVersion, ack.UploadBytes, ack.DownloadBytes = u.Version, u.UploadBytes, u.DownloadBytes
		ack.SlotReleased, ack.Closed = true, u.Closed
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	if err := a.releaseIdleOfflineSlot(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Grant.SlotReleased || s.Grant.Closed || s.Children["ended"].Normal != 100_000 {
		t.Fatal("revoked ended transport retained slot or lost unknown usage", s)
	}
	if err := s.updateChild("ended", model.LeaseUsage{Closed: true, DownloadBytes: 100, Billing: []billing.Segment{{Version: 1, Download: 100, AppliedAt: now}}}, now); err != nil {
		t.Fatal(err)
	}
	if err := a.syncOfflineState(context.Background(), &s, true); err != nil {
		t.Fatal(err)
	}
	if !s.Grant.Closed || s.Grant.DownloadBytes != 100 || s.Children["ended"].Normal != 15000 {
		t.Fatal("late final lost exact usage", s)
	}
}

func TestOfflineSlotWebsiteOutageKeepsHeldButCannotResurrectReleasedSlot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	for _, released := range []bool{false, true} {
		s := quotaTestState(now, "slot-parent", "hysteria2", 100_000_000)
		s.Grant.ConnectionSlotsVersion, s.Grant.SlotReleased = 1, released
		err := a.prepareOfflineSlot(context.Background(), &s)
		if released && err == nil || !released && err != nil {
			t.Fatal("outage changed central slot ownership", released, err)
		}
	}
}

func TestOfflineSlotUnavailableCoreStillReclaimsOnlyExpiredChildren(t *testing.T) {
	a := testAgent(t, "", "")
	now := a.now()
	s := quotaTestState(now, "slot-parent", "hysteria2", 100_000_000)
	s.Children["expired"] = offlineChild{ID: "expired", CoreGeneration: "old", Normal: 1000}
	s.Children["live"] = offlineChild{ID: "live", CoreGeneration: "old", Normal: 1000}
	a.offlineIssued = map[string]time.Time{"expired": time.Now().Add(-126 * time.Second), "live": time.Now()}
	a.reconcileOfflineChildren(context.Background(), &s, "")
	if !s.Children["expired"].Ended || s.Children["live"].Ended {
		t.Fatal("unavailable generation bypassed monotonic proof or prevented recovery", s.Children)
	}
}

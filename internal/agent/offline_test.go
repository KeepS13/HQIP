package agent

import (
	"context"
	"encoding/json"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/apernet/hysteria/core/v2/billing"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func offlineTestGrant(now time.Time) model.OfflineGrant {
	return model.OfflineGrant{Version: 1, ID: "grant-one", NodeID: "node-a", UserID: "u", SubscriptionID: "s", SessionID: "subscription", CredentialHash: secure.Hash(firstPassword), AuthorizedAt: now, OfflineUntil: now.Add(24 * time.Hour), NormalBudgetBytes: 64 << 20, EmergencyBudgetBytes: 32 << 20, RateVersion: 1, BandwidthBytesPerSecond: 100000}
}

func TestOfflineAdmissionPersistsAndReconnectsPast120Seconds(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	g := quotaTestState(now, "grant-one", "hysteria2", 64<<20*100).Grant
	g.ClientIP, g.ConnectionSlotsVersion = "198.51.100.1", model.ConnectionSlotsVersion
	status := 200
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/report") {
			var u model.OfflineUsage
			_ = json.NewDecoder(r.Body).Decode(&u)
			g.ReportVersion = u.Version
			g.UploadBytes = u.UploadBytes
			g.DownloadBytes = u.DownloadBytes
			g.EmergencyUsedBytes = u.EmergencyUsedBytes
			g.Closed = u.Closed
			g.Billing.Acknowledged = u.Billing
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	d := desired(1, firstPassword, true)
	d.PortalEnabled = true
	d.OfflineAdmissionVersion = 1
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	admit := func(ch string) *httptest.ResponseRecorder {
		return admissionRequest(a, "/admission", `{"clientIP":"198.51.100.1","billingCapability":"traffic-billing-v1","connectionId":"`+strings.Repeat(ch, 64)+`","password":"`+firstPassword+`"}`, "127.0.0.1:3000", testSecret)
	}
	w := admit("a")
	if w.Code != 200 {
		t.Fatal("initial", w.Code, w.Body.String())
	}
	var l model.ConnectionLease
	_ = json.Unmarshal(w.Body.Bytes(), &l)
	status = 503
	renew := func(id string, u model.LeaseUsage) *httptest.ResponseRecorder {
		if u.DownloadBytes > 0 {
			u.Billing = []billing.Segment{{Version: 1, Download: u.DownloadBytes, AppliedAt: g.AuthorizedAt}}
		}
		b, _ := json.Marshal(u)
		return admissionRequest(a, "/leases/"+id+"/renew", string(b), "127.0.0.1:3000", testSecret)
	}
	for i := 0; i < 5; i++ {
		now = now.Add(60 * time.Second)
		w = renew(l.ID, model.LeaseUsage{DownloadBytes: int64(i+1) * 1024, AppliedRateVersion: 1})
		if w.Code != 200 {
			t.Fatal("offline renewal", i, w.Code, w.Body.String())
		}
	}
	if w = admit("b"); w.Code == 200 {
		t.Fatal("duplicate credential stole occupied slot")
	}
	w = renew(l.ID, model.LeaseUsage{DownloadBytes: 5120, AppliedRateVersion: 1, Closed: true})
	if w.Code != 200 {
		t.Fatal("durable final", w.Code)
	}
	// Reopen the real SQLite ledger, preserving allocations and deadline.
	path := a.cfg.StatePath
	if err := a.ledger.close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	w = admit("c")
	if w.Code != 200 {
		t.Fatal("offline original node reconnect", w.Code, w.Body.String())
	}
	states, err := a.ledger.offlineStates()
	if err != nil || len(states) != 1 {
		t.Fatal(err)
	}
	if !states[0].Grant.OfflineUntil.Equal(g.OfflineUntil) || len(states[0].Children) != 2 {
		t.Fatal("restart reset grant", states)
	}
	// Explicit rejection must immediately deny local renewal, not look like a timeout.
	status = 403
	now = now.Add(31 * time.Second)
	var replacement model.ConnectionLease
	_ = json.Unmarshal(w.Body.Bytes(), &replacement)
	w = renew(replacement.ID, model.LeaseUsage{AppliedRateVersion: 1})
	if w.Code != 403 {
		t.Fatal("revocation treated as outage", w.Code)
	}
}

func TestOfflineLocalBudgetCrashAndEmergencyBoundaries(t *testing.T) {
	now := time.Now()
	g := offlineTestGrant(now)
	g.NormalBudgetBytes = 100
	g.EmergencyBudgetBytes = 50
	s := offlineState{Grant: g, Children: map[string]offlineChild{}, LastWall: now, Online: true}
	l, err := s.localGrant("a", now)
	if err != nil || l.BudgetBytes != 100 {
		t.Fatal("normal budget", l, err)
	}
	// A lost final cannot return the delegated normal bytes, even after expiry.
	s.Online = false
	now = now.Add(121 * time.Second)
	if _, err = s.localGrant("b", now); err == nil {
		t.Fatal("wall time alone released an unconfirmed slot")
	}
	old := s.Children["a"]
	old.Ended = true
	s.Children["a"] = old // reconciled core restart
	l, err = s.localGrant("b", now)
	if err != nil || l.BudgetBytes != 50 {
		t.Fatal("orphan reservation reused", l, err)
	}
	if err = s.updateChild("b", model.LeaseUsage{DownloadBytes: 50, Closed: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.localGrant("c", now); err == nil {
		t.Fatal("emergency pool reset on reconnect")
	}
	if err = s.updateChild("a", model.LeaseUsage{DownloadBytes: 10, Closed: true}, now); err != nil {
		t.Fatal(err)
	}
	l, err = s.localGrant("d", now)
	if err != nil || l.BudgetBytes != 90 {
		t.Fatal("confirmed unused normal bytes not returned", l, err)
	}
	u, err := s.usage(now)
	if err != nil || u.DownloadBytes != 60 || u.EmergencyUsedBytes != 50 {
		t.Fatal("aggregate usage", u, err)
	}
	if _, err = s.localGrant("d", g.OfflineUntil); err == nil {
		t.Fatal("24-hour boundary passed")
	}
	if _, err = s.localGrant("d", now.Add(-time.Second)); err == nil {
		t.Fatal("wall clock rollback allowed")
	}
}

func TestOfflineProtocolFailuresNeverEnableCache(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"invalid":true}`))
			}))
			defer central.Close()
			a := testAgent(t, "", central.URL)
			now := a.now()
			s := offlineState{Grant: offlineTestGrant(now), Children: map[string]offlineChild{}, LastWall: now}
			if err := a.syncOfflineState(context.Background(), &s, true); err == nil || !s.Denied {
				t.Fatal("protocol/authorization failure accepted", status, err)
			}
		})
	}
}

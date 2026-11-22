package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// A committed admission whose reply never arrived has no local parent yet.
// Background recovery must close it even if the client never tries again.
func TestOfflineRecoveryLostAdmissionWithoutClientRetry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	g := quotaTestState(now, "lost-admission", "hysteria2", 100_000_000).Grant
	g.ClientIP, g.ConnectionSlotsVersion = "198.51.100.1", model.ConnectionSlotsVersion
	connection := strings.Repeat("a", 64)
	var mu sync.Mutex
	closed, recovered := false, false
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/agents/offline-grants":
			w.WriteHeader(503) // Grant committed, response lost before any child exists.
		case "/api/v1/agents/offline-grants/recover":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["connectionId"] != connection || in["password"] != "" {
				t.Error("recovery changed the request or persisted a plaintext credential")
			}
			recovered = true
			_ = json.NewEncoder(w).Encode(g)
		default:
			var u model.OfflineUsage
			_ = json.NewDecoder(r.Body).Decode(&u)
			if !u.Closed || u.Active || u.UploadBytes != 0 || u.DownloadBytes != 0 {
				t.Error("undelivered admission was not settled as unused", u)
			}
			g.ReportVersion, g.Closed = u.Version, u.Closed
			closed = u.Closed
			_ = json.NewEncoder(w).Encode(g)
		}
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	a.cache.PortalEnabled, a.cache.OfflineAdmissionVersion, a.cache.NodeQuotaVersion = true, 1, 1
	w := admissionRequest(a, "/admission", `{"clientIP":"198.51.100.1","billingCapability":"traffic-billing-v1","connectionId":"`+connection+`","password":"`+firstPassword+`"}`, "127.0.0.1:3000", testSecret)
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	path := a.cfg.StatePath
	if err := a.ledger.close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.syncOfflineGrants(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !recovered || !closed {
		t.Fatal("lost admission still occupies a slot without client retry", recovered, closed)
	}
	states, err := a.ledger.offlineStates()
	if err != nil || len(states) != 1 || !states[0].Grant.Closed || len(states[0].Children) != 0 {
		t.Fatal("recovered accounting was not durable", states, err)
	}
}

// Every sweep has a finite WAN budget. Repeatedly starting at the first ID
// must not prevent a later disconnected parent from ever reporting release.
func TestOfflineSweepDoesNotStarveLaterIdleSlots(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	a := testAgent(t, "", "")
	a.now = func() time.Time { return now }
	a.cache.PortalEnabled, a.cache.NodeQuotaVersion = true, 1
	grants := map[string]model.OfflineGrant{}
	for _, id := range []string{"a-live", "b-ended"} {
		s := quotaTestState(now, id, "hysteria2", 100_000_000)
		s.Grant.ConnectionSlotsVersion, s.Online = model.ConnectionSlotsVersion, true
		s.Children[id] = offlineChild{ID: id, Ended: id == "b-ended", Until: now.Add(time.Hour), Normal: 1000, CoreGeneration: "core-a"}
		if err := a.ledger.saveOffline(s); err != nil {
			t.Fatal(err)
		}
		a.offlineIssued[id] = time.Now()
		grants[id] = s.Grant
	}
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		a.controlClient.Transport = offlineRecoveryTransport(func(r *http.Request) (*http.Response, error) {
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/agents/offline-grants/"), "/report")
			var u model.OfflineUsage
			_ = json.NewDecoder(r.Body).Decode(&u)
			g := grants[id]
			g.ReportVersion = u.Version
			g.SlotReleased = g.SlotReleased || u.SlotAction == "release"
			grants[id] = g
			w := httptest.NewRecorder()
			_ = json.NewEncoder(w).Encode(g)
			cancel() // One successful WAN request exhausts this sweep's budget.
			return w.Result(), nil
		})
		_ = a.syncOfflineGrants(ctx)
		cancel()
		now = now.Add(10 * time.Second)
	}
	if !grants["b-ended"].SlotReleased || grants["a-live"].SlotReleased {
		t.Fatal("later idle slot starved or live slot was released", grants)
	}
}

type offlineRecoveryTransport func(*http.Request) (*http.Response, error)

func (f offlineRecoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

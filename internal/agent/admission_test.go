package agent

import (
	"context"
	"encoding/json"
	"github.com/KeepS13/HQIP/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func admissionRequest(a *Agent, path, body, remote, secret string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestAdmissionActivationRevokesLegacyAndKeepsProbes(t *testing.T) {
	var centralCalls atomic.Int32
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { centralCalls.Add(1); http.Error(w, "denied", 403) }))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	d := desired(1, firstPassword, true)
	d.Devices = append(d.Devices, model.AgentDevice{ID: "__probe_test", Password: secondPassword, Enabled: true})
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	body := `{"clientIP":"198.51.100.1","billingCapability":"traffic-billing-v1","connectionId":"` + strings.Repeat("a", 64) + `","password":"` + firstPassword + `"}`
	w := admissionRequest(a, "/admission", body, "127.0.0.1:30000", testSecret)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"legacy":true`) {
		t.Fatal("legacy grant unavailable", w.Code)
	}
	d.Revision = 2
	d.PortalEnabled = true
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth(t, a, firstPassword); ok {
		t.Fatal("old auth endpoint bypassed activation")
	}
	if w = admissionRequest(a, "/admission", body, "127.0.0.1:30000", testSecret); w.Code != 403 {
		t.Fatal("activation retained old grant", w.Code)
	}
	if centralCalls.Load() != 1 {
		t.Fatal("admission did not consult central authority")
	}
	renewal := strings.TrimSuffix(body, "}") + `,"legacyRenewal":"true"}`
	if w = admissionRequest(a, "/admission", renewal, "127.0.0.1:30000", testSecret); w.Code != 403 || centralCalls.Load() != 1 {
		t.Fatal("legacy renew went central")
	}
	if w = admissionRequest(a, "/admission", strings.ReplaceAll(body, firstPassword, secondPassword), "127.0.0.1:30000", testSecret); w.Code != 200 {
		t.Fatal("probe compatibility lost")
	}
	a.cacheMu.RLock()
	pending := len(a.cache.Pending) > 0
	applied := a.cache.AppliedRevision
	a.cacheMu.RUnlock()
	if !pending || applied >= 2 {
		t.Fatal("activation acknowledged before legacy session drainage")
	}
	a.restartCore = func(context.Context) error { t.Error("portal revocation restarted other users"); return nil }
	_ = a.forceDrainIfOverdue(context.Background(), map[string]pendingRevocation{"phone": {Since: time.Now().Add(-time.Hour).UnixNano()}}, context.Canceled)
}

func TestAdmissionRequiresLoopbackAndPrivateSecret(t *testing.T) {
	a := testAgent(t, "", "")
	body := `{"connectionId":"` + strings.Repeat("a", 64) + `","password":"` + firstPassword + `"}`
	for _, tc := range []struct{ remote, secret string }{{"198.51.100.1:12", testSecret}, {"127.0.0.1:12", "wrong"}} {
		if w := admissionRequest(a, "/admission", body, tc.remote, tc.secret); w.Code != 403 {
			t.Fatal("untrusted admission accepted")
		}
	}
}

func TestAdmissionForwardsRateAcknowledgmentAndNodeIdentity(t *testing.T) {
	var calls atomic.Int32
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/leases/lease-1/renew" || r.Header.Get("Authorization") != "Bearer agent-token-that-is-long-enough" {
			t.Error("wrong central identity or path")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["appliedRateVersion"] != float64(7) || body["uploadBytes"] != float64(100) {
			t.Error("lost lease counter or applied rate ACK", body)
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"leaseId":"lease-1","uploadBytes":100,"downloadBytes":200,"ratePending":true,"rateVersion":8}`))
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	d := model.AgentDesired{Revision: 1, PortalEnabled: true}
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	body := `{"uploadBytes":100,"downloadBytes":200,"appliedRateVersion":7}`
	w := admissionRequest(a, "/leases/lease-1/renew", body, "127.0.0.1:1", testSecret)
	if w.Code != 200 || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"ratePending":true`) {
		t.Fatal("renewal contract broken", w.Code)
	}
	if w = admissionRequest(a, "/leases/lease-1/renew", `{"appliedRateVersion":-1}`, "127.0.0.1:1", testSecret); w.Code != 400 {
		t.Fatal("negative ACK accepted")
	}
}

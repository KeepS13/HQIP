package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func TestRealityAdmissionWithoutHY2AndGenerationIsolation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	password := strings.Repeat("p", 43)
	s := quotaTestState(now, "direct-parent", "vless", 100_000_000)
	g := s.Grant
	g.CredentialHash = secure.Hash(password)
	g.ClientIP = "198.51.100.1"
	g.ConnectionSlotsVersion = model.ConnectionSlotsVersion
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/offline-grants" {
			http.Error(w, "unexpected request", 503)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["billingProtocol"] != "vless" || body["clientIP"] != g.ClientIP || body["password"] != password {
			t.Error("lost trusted protocol/peer identity")
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.generation = func(context.Context) (string, error) {
		t.Error("direct admission queried HY2")
		return "", errors.New("HY2 down")
	}
	d := desired(1, password, true)
	d.PortalEnabled, d.OfflineAdmissionVersion, d.NodeQuotaVersion = true, 1, 1
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	ticket, err := a.realityBillingPassword(password, g.ClientIP)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"transport": "vless-direct", "clientIP": g.ClientIP, "billingCapability": "traffic-billing-v1", "connectionId": strings.Repeat("a", 64), "password": ticket})
	w := admissionRequest(a, "/admission", string(body), "127.0.0.1:3000", testSecret)
	if w.Code != 200 {
		t.Fatal("VLESS required HY2", w.Code, w.Body.String())
	}
	var grant model.ConnectionLease
	if err = json.Unmarshal(w.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	state, found, err := a.ledger.offlineChild(grant.ID)
	if err != nil || !found {
		t.Fatal("missing durable direct authorization", err)
	}
	child := state.Children[grant.ID]
	if !strings.HasPrefix(child.CoreGeneration, "vless:") {
		t.Fatal("direct session belongs to HY2")
	}
	a.offlineMu.Lock()
	defer a.offlineMu.Unlock()
	for _, generation := range []string{"", "hy2-before", "hy2-after"} {
		a.reconcileOfflineChildren(context.Background(), &state, generation)
		if state.Children[grant.ID].Ended {
			t.Fatal("HY2 stop/restart ended VLESS")
		}
	}
	state.Children["old-hy2"] = offlineChild{ID: "old-hy2", CoreGeneration: "hy2-before", Normal: 1024}
	a.reconcileOfflineChildren(context.Background(), &state, "hy2-after")
	if !state.Children["old-hy2"].Ended || state.Children[grant.ID].Ended {
		t.Fatal("protocol generation boundary lost")
	}
	a.realityGeneration = "" // A new Agent instance owns a fresh generation.
	a.reconcileOfflineChildren(context.Background(), &state, "hy2-after")
	if !state.Children[grant.ID].Ended || state.Children[grant.ID].Closed || state.Children[grant.ID].Normal != child.Normal {
		t.Fatal("restart fabricated final usage or returned unknown budget")
	}
}

func TestRealityReportsCapabilitiesWhileHY2IsUnavailable(t *testing.T) {
	reports := make(chan model.AgentReport, 1)
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agents/report" {
			var report model.AgentReport
			_ = json.NewDecoder(r.Body).Decode(&report)
			reports <- report
		}
		_ = json.NewEncoder(w).Encode(model.AgentDesired{Revision: 1, PortalEnabled: true, OfflineAdmissionVersion: 1, NodeQuotaVersion: 1})
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.realityEndpoint = &model.RealityEndpoint{Port: 443, ServerName: model.DefaultRealityServerName}
	a.generation = func(context.Context) (string, error) { return "", errors.New("HY2 stopped") }
	if err := a.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	report := <-reports
	if report.CoreHealthy || report.Reality == nil {
		t.Fatal("HY2 health replaced independent Reality readiness")
	}
	for _, capability := range []string{model.RealityCapability, "admission-v1", "traffic-billing-v1", model.NodeQuotaCapability, "connection-slots-v3"} {
		if !slices.Contains(report.Capabilities, capability) {
			t.Fatal("missing independently usable capability", capability)
		}
	}
}

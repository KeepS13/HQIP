package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestAdmissionIdentityIsReadOnlyAndLocallyAuthenticated(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/admission/check" {
			t.Error("check reached mutating admission", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "accountingId": "subscription_s"})
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.cache.PortalEnabled = true
	body := `{"clientIP":"192.0.2.1","billingCapability":"traffic-billing-v1","connectionId":"` + strings.Repeat("a", 64) + `","password":"` + firstPassword + `"}`
	for _, remote := range []string{"192.0.2.1:1000", "127.0.0.1:1000"} {
		w := admissionRequest(a, "/admission/check", body, remote, testSecret)
		want := 403
		if strings.HasPrefix(remote, "127.") {
			want = 200
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := admissionRequest(a, "/admission/check", body, "127.0.0.1:1000", "wrong"); w.Code != 403 {
		t.Fatal("unauthenticated check")
	}
	states, _ := a.ledger.offlineStates()
	requests, _ := a.ledger.offlineRequests("")
	if len(states) != 0 || len(requests) != 0 {
		t.Fatal("identity check wrote admission ledger")
	}
}

func TestAdmissionIdentityOutageRequiresExistingHeldIdentity(t *testing.T) {
	for _, tc := range []string{"held", "released", "other_ip", "denied", "revoked"} {
		t.Run(tc, func(t *testing.T) {
			a := testAgent(t, "", "http://127.0.0.1:1")
			a.cache.PortalEnabled = true
			s := quotaTestState(time.Now(), "identity-held", "hysteria2", 100_000_000)
			s.Grant.ClientIP = "192.0.2.1"
			s.Grant.ConnectionSlotsVersion = model.ConnectionSlotsVersion
			s.Grant.SlotReleased = tc == "released"
			s.Denied = tc == "denied"
			s.Grant.Revoked = tc == "revoked"
			if tc == "other_ip" {
				s.Grant.ClientIP = "192.0.2.2"
			}
			if err := a.ledger.saveOffline(s); err != nil {
				t.Fatal(err)
			}
			body := `{"clientIP":"192.0.2.1","billingCapability":"traffic-billing-v1","connectionId":"` + strings.Repeat("a", 64) + `","password":"` + firstPassword + `"}`
			w := admissionRequest(a, "/admission/check", body, "127.0.0.1:1000", testSecret)
			if (w.Code == 200) != (tc == "held") {
				t.Fatal("incorrect outage identity", tc, w.Code, w.Body.String())
			}
		})
	}
}

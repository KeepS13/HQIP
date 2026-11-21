package agent

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func TestOnlineIPOfflineCacheCannotAuthorizeAnotherIP(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer central.Close()
	for _, released := range []bool{false, true} {
		a := testAgent(t, "", central.URL)
		now := time.Now()
		a.now = func() time.Time { return now }
		d := desired(1, firstPassword, true)
		d.PortalEnabled, d.OfflineAdmissionVersion, d.NodeQuotaVersion = true, 1, 1
		if err := a.applyDesired(d); err != nil {
			t.Fatal(err)
		}
		s := quotaTestState(now, "cached-ip", "hysteria2", 100_000_000)
		s.Grant.ClientIP, s.Grant.ConnectionSlotsVersion = "198.51.100.1", model.ConnectionSlotsVersion
		s.Grant.SlotReleased = released
		if err := a.ledger.saveOffline(s); err != nil {
			t.Fatal(err)
		}
		for i, ip := range []string{"198.51.100.2", "198.51.100.1"} {
			body, _ := json.Marshal(map[string]string{"connectionId": strings.Repeat(string(rune('a'+i)), 64), "password": firstPassword, "clientIP": ip, "billingCapability": billing.Capability})
			w := admissionRequest(a, "/admission", string(body), "127.0.0.1:42", testSecret)
			wantAllowed := !released && i == 1
			if (w.Code == 200) != wantAllowed {
				t.Fatalf("ip=%s released=%v status=%d: %s", ip, released, w.Code, w.Body.String())
			}
		}
	}
}

func TestOnlineIPRealityAddressIsSigned(t *testing.T) {
	a := &Agent{cfg: model.AgentConfig{NodeID: "node-a", Token: strings.Repeat("k", 32)}, now: time.Now}
	credential, err := a.realityBillingPassword(strings.Repeat("p", 43), "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(credential, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	raw = []byte(strings.Replace(string(raw), "198.51.100.1", "198.51.100.2", 1))
	parts[1] = base64.RawURLEncoding.EncodeToString(raw)
	if _, _, _, err = a.billingIdentity(strings.Join(parts, "."), billing.Capability, "127.0.0.1"); err == nil {
		t.Fatal("forged source IP accepted")
	}
}

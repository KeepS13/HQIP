package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Frequent completed probes must not make a usable subscription permanently
// stick on its first full parent. Retain every old child/counter for late final.
func TestOfflineAdmissionSkipsFullReleasedParent(t *testing.T) {
	now := time.Now()
	s := quotaTestState(now, "full-parent", "hysteria2", 100_000_000)
	s.Grant.ClientIP = "198.51.100.1"
	s.Grant.ConnectionSlotsVersion = model.ConnectionSlotsVersion
	s.Grant.SlotReleased = true
	for i := 0; i < 4096; i++ {
		id := fmt.Sprintf("old-%d", i)
		s.Children[id] = offlineChild{ID: id, Closed: true}
	}
	g := quotaTestState(now, "new-parent", "hysteria2", 100_000_000).Grant
	g.ClientIP = s.Grant.ClientIP
	g.ConnectionSlotsVersion = model.ConnectionSlotsVersion
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/report") {
			t.Error("full released parent was reacquired")
			http.Error(w, "unexpected", 409)
			return
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.cache.PortalEnabled = true
	a.cache.OfflineAdmissionVersion = 1
	a.cache.NodeQuotaVersion = 1
	if err := a.ledger.saveOffline(s); err != nil {
		t.Fatal(err)
	}
	body := `{"clientIP":"198.51.100.1","billingCapability":"traffic-billing-v1","connectionId":"` + strings.Repeat("a", 64) + `","password":"` + firstPassword + `"}`
	w := admissionRequest(a, "/admission", body, "127.0.0.1:1000", testSecret)
	if w.Code != 200 {
		t.Fatal("full historical parent stranded new traffic", w.Code, w.Body.String())
	}
	states, _ := a.ledger.offlineStates()
	if len(states) != 2 {
		t.Fatal("historical accounting was discarded", len(states))
	}
	for _, state := range states {
		if state.Grant.ID == s.Grant.ID && (len(state.Children) != 4096 || !state.Grant.SlotReleased) {
			t.Fatal("old parent mutated")
		}
	}
}

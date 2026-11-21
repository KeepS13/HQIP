package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func TestLegacyMigrationSettlesBeforeProtocolAdmission(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	old := offlineTestGrant(now)
	var reports []model.OfflineUsage
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/report") {
			var u model.OfflineUsage
			_ = json.NewDecoder(r.Body).Decode(&u)
			reports = append(reports, u)
			old.ReportVersion, old.UploadBytes, old.DownloadBytes = u.Version, u.UploadBytes, u.DownloadBytes
			old.Closed = u.Closed
			_ = json.NewEncoder(w).Encode(old)
			return
		}
		if !old.Closed {
			http.Error(w, "old reservation still owns quota", 403)
			return
		}
		g := offlineTestGrant(now)
		g.ID = "protocol-grant"
		g.Billing = &billing.Grant{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, BudgetUnits: g.NormalBudgetBytes * 100, EmergencyBytes: g.EmergencyBudgetBytes}
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	d := desired(1, firstPassword, true)
	d.PortalEnabled, d.OfflineAdmissionVersion = true, 1
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	// A lost previous response must replay byte-for-byte before the final.
	pending := model.OfflineUsage{Version: 1, DownloadBytes: 7, AppliedRateVersion: 1, Active: true}
	s := offlineState{Grant: old, Children: map[string]offlineChild{"old": {ID: "old", Download: 7, Normal: 7, Closed: true}}, LastWall: now, Online: true, NextVersion: 1, Pending: &pending}
	if err := a.ledger.saveOffline(s); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"connectionId": strings.Repeat("b", 64), "password": firstPassword, "billingCapability": "traffic-billing-v1"})
	w := admissionRequest(a, "/admission", string(body), "127.0.0.1:3000", testSecret)
	if w.Code != 200 {
		t.Fatalf("protocol admission %d: %s", w.Code, w.Body.String())
	}
	if len(reports) != 2 || reports[0].Version != 1 || !reports[0].Active || reports[0].Closed || reports[1].Version != 2 || !reports[1].Closed || reports[1].DownloadBytes != 7 {
		t.Fatalf("unsafe migration reports: %+v", reports)
	}
}

func TestLegacyMigrationRetainsUnconfirmedChildren(t *testing.T) {
	for _, ended := range []bool{false, true} {
		a := testAgent(t, "", "http://127.0.0.1:1")
		now := a.now()
		s := offlineState{Grant: offlineTestGrant(now), Children: map[string]offlineChild{"unknown": {ID: "unknown", Ended: ended, Until: now.Add(-time.Hour), Normal: 100}}, LastWall: now}
		if err := a.retireIdleLegacyGrant(context.Background(), &s); err != nil || s.Denied || s.Grant.Closed {
			t.Fatal("unconfirmed child retired", ended, err)
		}
	}
}

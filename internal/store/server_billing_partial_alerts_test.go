package store

import (
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestIncompleteCycleUsageStillAlertsAtKnownThresholds(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	server, node, _ := serverPair(t, s)
	quota := int64(1000)
	var err error
	server, err = s.UpdateServerSettings(server.ID, server.Name, nil, &quota, server.Revision)
	if err != nil {
		t.Fatal(err)
	}
	server = configureBilling(t, s, server, now.Add(30*24*time.Hour))
	if _, err = s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.monthlyQuotaBytes',?) WHERE id=?", quota, node.ID); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, tc := range []struct {
		used  int64
		stage int
		level string
	}{
		{0, 0, ""}, {799, 0, ""}, {800, 80, "warning"}, {999, 80, "warning"}, {1000, 100, "danger"}, {1200, 100, "danger"},
	} {
		rawBillingReport(t, s, node.ID, *now, tc.used/2, tc.used-tc.used/2)
		// A stale node makes this cycle incomplete without losing known ledger bytes.
		if _, err = s.DB.Exec("UPDATE nodes SET last_seen=? WHERE id=?", now.Add(-time.Minute).Unix(), node.ID); err != nil {
			t.Fatal(err)
		}
		current, err := s.Server(server.ID)
		if err != nil {
			t.Fatal(err)
		}
		n, err := s.Node(node.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, billing := range []model.ServerBilling{current.Billing, n.Billing} {
			if billing.CycleComplete || billing.CycleUpload+billing.CycleDownload != tc.used || billing.CycleID != server.Billing.CycleID {
				t.Fatalf("known usage/cycle changed: %+v, want %d", billing, tc.used)
			}
		}
		conditions, err := s.CollectNotificationConditions()
		if err != nil {
			t.Fatal(err)
		}
		if err = s.EvaluateAlerts(conditions); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"server.cycle", "node.cycle"} {
			var found []model.Alert
			for _, item := range alertList(t, s) {
				if item.Condition.Kind == kind {
					found = append(found, item)
				}
			}
			if tc.stage == 0 {
				if len(found) != 0 {
					t.Fatalf("%s invented a capacity warning for %d bytes", kind, tc.used)
				}
				continue
			}
			if len(found) != 1 {
				t.Fatalf("%s expected one warning for %d bytes: %+v", kind, tc.used, found)
			}
			item := found[0]
			if item.Status != "active" || item.Condition.Unknown || item.Condition.Stage != tc.stage || item.Condition.Level != tc.level || !strings.Contains(item.Condition.Message, "数据不完整") {
				t.Fatalf("%s suppressed/escalated incorrectly at %d bytes: %+v", kind, tc.used, item)
			}
			if previous := ids[kind]; previous != "" && previous != item.ID {
				t.Fatal("threshold escalation duplicated cycle alert")
			}
			ids[kind] = item.ID
		}
	}
	// Unlimited capacities retain the same counters but no active quota warning.
	current, _ := s.Server(server.ID)
	if _, err = s.UpdateServerSettings(current.ID, current.Name, nil, nil, current.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.monthlyQuotaBytes',NULL) WHERE id=?", node.ID); err != nil {
		t.Fatal(err)
	}
	conditions, err := s.CollectNotificationConditions()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EvaluateAlerts(conditions); err != nil {
		t.Fatal(err)
	}
	for _, item := range alertList(t, s) {
		if (item.Condition.Kind == "server.cycle" || item.Condition.Kind == "node.cycle") && item.Status != "ended" {
			t.Fatal("unlimited quota kept capacity alert active", item)
		}
	}
}

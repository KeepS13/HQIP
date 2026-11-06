package store

import (
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func collectBillingAlerts(t *testing.T, s *Store, kind string) []model.Alert {
	t.Helper()
	conditions, err := s.CollectNotificationConditions()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EvaluateAlerts(conditions); err != nil {
		t.Fatal(err)
	}
	all := alertList(t, s)
	out := []model.Alert{}
	for _, a := range all {
		if a.Condition.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

func TestServerExpiryAlertsEscalateResetAcknowledgementAndEndAfterRenewal(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	expiry := now.Add(72 * time.Hour)
	v, err := s.CreateServer(model.Server{Name: "expiring-server", SSHHost: "203.0.113.35", Billing: model.ServerBilling{ExpiresAt: &expiry}}, "initial-admin")
	if err != nil {
		t.Fatal(err)
	}
	if got := collectBillingAlerts(t, s, "server.expiry"); len(got) != 0 {
		t.Fatal("expiry alert too early")
	}
	var event string
	for index, hours := range []int{48, 24, 12, 0} {
		*now = expiry.Add(-time.Duration(hours) * time.Hour)
		items := collectBillingAlerts(t, s, "server.expiry")
		if len(items) != 1 || items[0].Status != "active" || items[0].Condition.Stage != index+1 || items[0].Acknowledged {
			t.Fatal("expiry threshold lost escalation", items)
		}
		if event != "" && items[0].ID != event {
			t.Fatal("same expiry created duplicate event")
		}
		event = items[0].ID
		if _, err = s.UpdateAlert(event, "operator", model.AlertUpdate{ExpectedRevision: items[0].Revision, Acknowledge: true}); err != nil {
			t.Fatal(err)
		}
		if repeated := collectBillingAlerts(t, s, "server.expiry"); len(repeated) != 1 || !repeated[0].Acknowledged {
			t.Fatal("same threshold reset acknowledgement")
		}
	}
	newExpiry := now.Add(96 * time.Hour)
	if _, _, err = s.RenewServer(v.ID, ServerRenewalRequest{ExpectedRevision: v.Revision, RequestID: "manual-alert-renew", ExpiresAt: &newExpiry}, "operator"); err != nil {
		t.Fatal(err)
	}
	items := collectBillingAlerts(t, s, "server.expiry")
	if len(items) != 1 || items[0].Status != "ended" || items[0].ID != event {
		t.Fatal("old expiry did not end after manual renewal", items)
	}
	*now = newExpiry.Add(-48 * time.Hour)
	items = collectBillingAlerts(t, s, "server.expiry")
	var active int
	for _, item := range items {
		if item.Status == "active" {
			active++
			if item.ID == event || item.Acknowledged {
				t.Fatal("new expiry reused past acknowledgement")
			}
		}
	}
	if len(items) != 2 || active != 1 {
		t.Fatal("new expiry cycle not isolated", items)
	}
}

func TestServerCycleCapacityAlertsDedupeAndRenewalBoundary(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	v, a, _ := serverPair(t, s)
	quota := int64(100)
	var err error
	v, err = s.UpdateServerSettings(v.ID, v.Name, nil, &quota, v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(48 * time.Hour)
	v = configureBilling(t, s, v, expiry)
	rawBillingReport(t, s, a.ID, *now, 40, 40)
	items := collectBillingAlerts(t, s, "server.cycle")
	if len(items) != 1 || items[0].Condition.Stage != 80 {
		t.Fatal("cycle quota not emitted", items)
	}
	old := items[0]
	if _, err = s.UpdateAlert(old.ID, "operator", model.AlertUpdate{ExpectedRevision: old.Revision, Acknowledge: true}); err != nil {
		t.Fatal(err)
	}
	months := 1
	v, _, err = s.RenewServer(v.ID, ServerRenewalRequest{ExpectedRevision: v.Revision, RequestID: "prepaid-alert-renew", Months: &months}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	items = collectBillingAlerts(t, s, "server.cycle")
	if len(items) != 1 || items[0].ID != old.ID || !items[0].Acknowledged {
		t.Fatal("prepaid renewal reset current warning", items)
	}
	*now = expiry.Add(10 * time.Minute)
	rawBillingReport(t, s, a.ID, *now, 400, 400, model.UsageBucket{Start: expiry.Add(-time.Hour).Unix(), Upload: 360, Download: 360}, model.UsageBucket{Start: expiry.Unix(), Upload: 40, Download: 40})
	items = collectBillingAlerts(t, s, "server.cycle")
	var active int
	for _, item := range items {
		if item.Status == "active" {
			active++
			if item.ID == old.ID || item.Acknowledged || item.Condition.Stage != 80 {
				t.Fatal("new cycle inherited old quota state", item)
			}
		} else if item.ID == old.ID && item.Status != "ended" {
			t.Fatal("old cycle not ended", item)
		}
	}
	if len(items) != 2 || active != 1 {
		t.Fatal("new cycle deduplication incorrect", items)
	}
}

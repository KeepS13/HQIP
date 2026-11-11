package store

import (
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodeLegacyCapacityDoesNotChangeTrafficOrReappear(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "meter-only")
	d := newDevice(t, s, "meter-user")
	report(t, s, n, d, *now, 120, 80)
	// Simulate an existing record written before node capacity was retired.
	notificationExec(t, s, "UPDATE nodes SET config=json_set(config,'$.bandwidthMbps',1,'$.monthlyQuotaBytes',10) WHERE id=?", n.ID)
	n, err := s.Node(n.ID)
	if err != nil || n.BandwidthMbps != nil || n.MonthlyQuotaBytes != nil || n.MonthUpload != 120 || n.MonthDownload != 80 || n.TotalUpload != 120 || n.TotalDownload != 80 {
		t.Fatalf("legacy capacity exposed or metering changed: %+v, %v", n, err)
	}
	desired, applied := n.DesiredRevision, n.AppliedRevision
	n.Name = "renamed"
	bw, quota := 2.0, int64(1)
	n.BandwidthMbps, n.MonthlyQuotaBytes = &bw, &quota
	if err = s.SaveNode(n); err != nil {
		t.Fatal(err)
	}
	n, err = s.Node(n.ID)
	if err != nil || n.Name != "renamed" || n.DesiredRevision != desired || n.AppliedRevision != applied || n.TotalUpload+n.TotalDownload != 200 {
		t.Fatalf("rename changed deployment or traffic: %+v, %v", n, err)
	}
	var capacities int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=? AND (json_extract(config,'$.bandwidthMbps') IS NOT NULL OR json_extract(config,'$.monthlyQuotaBytes') IS NOT NULL)", n.ID).Scan(&capacities); err != nil || capacities != 0 {
		t.Fatalf("stale update restored stored limits: %d, %v", capacities, err)
	}
	created, err := s.CreateNode(model.Node{Name: "unlimited", SSHHost: "meter2.example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.9", Domain: "meter2.example.com", Port: 443, BandwidthMbps: &bw, MonthlyQuotaBytes: &quota}, "secret")
	if err != nil || created.BandwidthMbps != nil || created.MonthlyQuotaBytes != nil {
		t.Fatalf("creation retained obsolete node limits: %+v, %v", created, err)
	}
}

func TestRetiredNodeQuotaAlertsEndWithoutRequiringAcknowledgement(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "old-capacity-alerts")
	for _, kind := range []string{"node.month", "node.cycle"} {
		alertEval(t, s, model.NotificationCondition{Key: kind + ":" + n.ID + ":old-period", Kind: kind, ResourceID: n.ID, Category: "capacity", Title: "旧节点流量提醒", Message: "100%", Stage: 100, Level: "danger"})
	}
	before := alertList(t, s)
	if len(before) != 2 {
		t.Fatalf("missing historical alerts: %+v", before)
	}
	conditions, err := s.CollectNotificationConditions()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EvaluateAlerts(conditions); err != nil {
		t.Fatal(err)
	}
	items := alertList(t, s)
	if len(items) != 2 {
		t.Fatalf("retired alerts lost their history: %+v", items)
	}
	for _, a := range items {
		if a.Status != "ended" || a.AcknowledgementRequired || a.Acknowledged || a.EndedAt == nil {
			t.Fatalf("obsolete alert still active or fabricated acknowledgement: %+v", a)
		}
		detail, err := s.Alert(a.ID)
		if err != nil || len(detail.Activities) != 2 || detail.Activities[0].Kind != "ended" {
			t.Fatalf("missing system ending/history: %+v, %v", detail, err)
		}
	}
	summary, err := s.AlertSummary()
	if err != nil || summary.Active != 0 || summary.Unacknowledged != 0 {
		t.Fatalf("retired alerts pollute summary: %+v, %v", summary, err)
	}
}

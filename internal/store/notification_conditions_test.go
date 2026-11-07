package store

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func notificationExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func notificationCollect(t *testing.T, s *Store) map[string]model.NotificationCondition {
	t.Helper()
	conditions, err := s.CollectNotificationConditions()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.NotificationCondition{}
	for _, c := range conditions {
		if _, exists := out[c.Key]; exists {
			t.Fatalf("duplicate condition %s", c.Key)
		}
		out[c.Key] = c
	}
	return out
}

func notificationKinds(all map[string]model.NotificationCondition, kind string) []model.NotificationCondition {
	out := []model.NotificationCondition{}
	for _, c := range all {
		if c.Kind == kind && !c.Removed {
			out = append(out, c)
		}
	}
	return out
}

func TestNotificationCapacityLedgerMonthTombstonesAndQuotaChanges(t *testing.T) {
	s, now := fixture(t)
	v, a, b := serverPair(t, s)
	quota := int64(1000)
	if _, err := s.UpdateServerSettings(v.ID, v.Name, nil, &quota, v.Revision); err != nil {
		t.Fatal(err)
	}
	for _, n := range []model.Node{a, b} {
		notificationExec(t, s, "UPDATE nodes SET config=json_set(config,'$.monthlyQuotaBytes',1000) WHERE id=?", n.ID)
	}
	d := newDevice(t, s, "accounting")
	report(t, s, a, d, *now, 500, 300)
	report(t, s, b, d, *now, 200, 0)
	// Duplicate reports and probes are excluded by the same durable ledger used
	// by the dashboard, not reconstructed from rates or current device lists.
	report(t, s, a, d, *now, 500, 300)
	if err := s.Report(a.ID, model.AgentReport{CoreHealthy: true, Daily: []model.UsageBucket{{DeviceID: ProbeID(a.ID), Start: DayStart(*now), Upload: 99999, Download: 99999}}}); err != nil {
		t.Fatal(err)
	}
	all := notificationCollect(t, s)
	serverKey := "server.month:" + v.ID + ":2026-10"
	if all[serverKey].Stage != 100 || all["node.month:"+a.ID+":2026-10"].Stage != 80 {
		t.Fatalf("wrong highest thresholds: %+v", all)
	}
	if len(notificationKinds(all, "node.month")) != 1 {
		t.Fatal("unexpected node contributions")
	}
	if !strings.Contains(all[serverKey].Message, "仅提醒，不限速、不断网") {
		t.Fatal("capacity semantics missing")
	}
	notificationExec(t, s, "UPDATE nodes SET lifecycle='deleted',deleted_at=? WHERE id=?", now.Unix(), a.ID)
	if _, err := s.DeleteDevice(d.ID); err != nil {
		t.Fatal(err)
	}
	all = notificationCollect(t, s)
	if all[serverKey].Stage != 100 || len(notificationKinds(all, "node.month")) != 0 {
		t.Fatal("deleted node excluded from server history or still alerted")
	}
	// Late upload/download increments remain in their original node history.
	report(t, s, a, d, *now, 550, 350)
	if !strings.Contains(notificationCollect(t, s)[serverKey].Message, "1.10 KB") {
		t.Fatal("late history missing")
	}
	*now = now.Add(2 * time.Minute)
	all = notificationCollect(t, s)
	if len(notificationKinds(all, "server.month")) != 0 {
		t.Fatal("Beijing month did not reset advisory threshold")
	}
	report(t, s, b, d, *now, 200, 100)
	notificationExec(t, s, "UPDATE servers SET config=json_set(config,'$.monthlyQuotaBytes',250) WHERE id=?", v.ID)
	all = notificationCollect(t, s)
	if all["server.month:"+v.ID+":2026-11"].Stage != 100 {
		t.Fatal("lowering quota did not immediately alert")
	}
	notificationExec(t, s, "UPDATE servers SET config=json_set(config,'$.monthlyQuotaBytes',null) WHERE id=?", v.ID)
	if len(notificationKinds(notificationCollect(t, s), "server.month")) != 0 {
		t.Fatal("unlimited quota alerted")
	}
	// Same IP rebuilt under a new ID adds only its own new ledger contribution.
	notificationExec(t, s, "UPDATE nodes SET config=json_set(config,'$.domain','retired.example.com') WHERE id=?", a.ID)
	rebuilt, err := s.CreateNode(model.Node{Name: "rebuilt", ServerID: v.ID, AddressID: a.AddressID, Domain: "rebuilt.example.com", Port: 443}, "password")
	if err != nil {
		t.Fatal(err)
	}
	rebuiltDevice := newDevice(t, s, "rebuilt-device")
	report(t, s, rebuilt, rebuiltDevice, *now, 10, 20)
	notificationExec(t, s, "UPDATE servers SET config=json_set(config,'$.monthlyQuotaBytes',400) WHERE id=?", v.ID)
	all = notificationCollect(t, s)
	if all["server.month:"+v.ID+":2026-11"].Stage != 80 {
		t.Fatalf("rebuild inflated or lost accounting %+v", all)
	}
}

func TestNotificationHealthGraceAndDiskHostObservation(t *testing.T) {
	s, now := fixture(t)
	v, a, b := serverPair(t, s)
	all := notificationCollect(t, s)
	if len(notificationKinds(all, "node.health")) != 0 {
		t.Fatal("uninstalled node alerted")
	}
	metrics := model.Metrics{DiskTotal: 1 << 30, DiskUsed: (1 << 30) - (128 << 20)}
	for _, n := range []model.Node{a, b} {
		if err := s.Report(n.ID, model.AgentReport{CoreHealthy: true, Metrics: metrics}); err != nil {
			t.Fatal(err)
		}
	}
	all = notificationCollect(t, s)
	diskKey := "server.disk:" + v.ID
	if len(notificationKinds(all, "server.disk")) != 1 || all[diskKey].Unknown {
		t.Fatal("disk must deduplicate physical server")
	}
	notificationExec(t, s, "UPDATE nodes SET core_healthy=0 WHERE id=?", a.ID)
	health := notificationCollect(t, s)["node.health:"+a.ID]
	if health.DelaySeconds != 60 || health.RecoverySeconds != 30 || health.Unknown {
		t.Fatalf("core debounce missing: %+v", health)
	}
	*now = now.Add(31 * time.Second)
	all = notificationCollect(t, s)
	if !all[diskKey].Unknown || !all["node.health:"+a.ID].Unknown {
		t.Fatal("stale telemetry falsely recovered")
	}
	*now = now.Add(29 * time.Second)
	all = notificationCollect(t, s)
	if c := all["node.health:"+a.ID]; c.Unknown || c.DelaySeconds != 0 || !strings.Contains(c.Message, "60 秒") {
		t.Fatalf("heartbeat threshold: %+v", c)
	}
	metrics.DiskUsed = 100
	if err := s.Report(a.ID, model.AgentReport{CoreHealthy: true, Metrics: metrics}); err != nil {
		t.Fatal(err)
	}
	all = notificationCollect(t, s)
	if _, exists := all["node.health:"+a.ID]; exists {
		t.Fatal("healthy node still bad")
	}
	if _, exists := all[diskKey]; exists {
		t.Fatal("fresh healthy disk did not resolve")
	}
	metrics.DiskUsed = metrics.DiskTotal + 1
	if err := s.Report(a.ID, model.AgentReport{CoreHealthy: true, Metrics: metrics}); err != nil {
		t.Fatal(err)
	}
	if !notificationCollect(t, s)[diskKey].Unknown {
		t.Fatal("impossible capacity treated as full or healthy")
	}
	notificationExec(t, s, "UPDATE nodes SET lifecycle='deleted',deleted_at=?", now.Unix())
	all = notificationCollect(t, s)
	if len(notificationKinds(all, "node.health")) != 0 {
		t.Fatal("deleted nodes still emitted heartbeat alarms")
	}
	if !all["node.health:"+a.ID].Removed || !all["certificate.application:"+a.ID].Removed || !all["certificate.issuance:"+a.ID].Removed {
		t.Fatal("node removal must cancel incidents without a recovery")
	}
	notificationExec(t, s, "UPDATE servers SET deleted_at=? WHERE id=?", now.Unix(), v.ID)
	all = notificationCollect(t, s)
	if len(notificationKinds(all, "server.disk")) != 0 {
		t.Fatal("deleted server remained an incident")
	}
	if !all[diskKey].Removed {
		t.Fatal("server removal must cancel disk incident without a recovery")
	}
}

func TestNotificationCertificatesExpiryStagesAndSafeErrors(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "cert\nnode")
	for _, tt := range []struct {
		days  int
		stage int
	}{{15, 0}, {14, 1}, {7, 2}, {3, 3}, {0, 4}, {-1, 4}} {
		after := now.Add(time.Duration(tt.days) * 24 * time.Hour).Unix()
		notificationExec(t, s, "INSERT INTO certificate_state(node_id,not_after) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET not_after=excluded.not_after", n.ID, after)
		conditions := notificationKinds(notificationCollect(t, s), "certificate.expiry")
		if tt.stage == 0 {
			if len(conditions) != 0 {
				t.Fatal("early certificate expiry")
			}
			continue
		}
		if len(conditions) != 1 || conditions[0].Stage != tt.stage || !strings.Contains(conditions[0].Key, strconv.FormatInt(after, 10)) {
			t.Fatalf("expiry %d: %+v", tt.days, conditions)
		}
	}
	notificationExec(t, s, "UPDATE certificate_state SET error=?,applied_error=?,issued_version=3 WHERE node_id=?", "secret raw webhook https://example.com/private", "-----BEGIN PRIVATE KEY-----", n.ID)
	all := notificationCollect(t, s)
	if len(notificationKinds(all, "certificate.issuance")) != 1 || len(notificationKinds(all, "certificate.application")) != 1 {
		t.Fatal("certificate errors missing")
	}
	for _, c := range all {
		if strings.Contains(c.Message, "PRIVATE") || strings.Contains(c.Message, "webhook") || strings.ContainsAny(c.Title, "\r\n") {
			t.Fatalf("sensitive or multiline data: %+v", c)
		}
	}
	notificationExec(t, s, "UPDATE nodes SET lifecycle='deleted',deleted_at=? WHERE id=?", now.Unix(), n.ID)
	for _, c := range notificationCollect(t, s) {
		if c.Category == "certificate" && !c.Removed {
			t.Fatal("deleted certificate still alerts")
		}
	}
}

func TestInfrastructureAlertsDoNotDependOnUserSubscriptionTables(t *testing.T) {
	s, _ := fixture(t)
	notificationExec(t, s, "DROP TABLE portal_usage")
	conditions, err := s.CollectNotificationConditions()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range conditions {
		if c.Category == "package" || strings.HasPrefix(c.DetailPath, "/users/") {
			t.Fatal("user alert leaked into infrastructure center")
		}
	}
}

func TestNotificationSnapshotErrorNeverMeansRecovery(t *testing.T) {
	for _, table := range []string{"daily", "certificate_state"} {
		t.Run(table, func(t *testing.T) {
			s, _ := fixture(t)
			notificationExec(t, s, "DROP TABLE "+table)
			if conditions, err := s.CollectNotificationConditions(); err == nil || conditions != nil {
				t.Fatal("partial snapshot escaped as successful")
			}
		})
	}
	t.Run("invalid-config", func(t *testing.T) {
		s, _ := fixture(t)
		n := newNode(t, s, "broken")
		_, writeErr := s.DB.Exec("UPDATE nodes SET config='invalid' WHERE id=?", n.ID)
		if s.postgresDSN != "" {
			if writeErr == nil {
				t.Fatal("PostgreSQL accepted malformed node JSON")
			}
			return
		}
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, err := s.CollectNotificationConditions(); err == nil {
			t.Fatal("malformed node config ignored")
		}
	})

}

func TestNotificationStoppedCertificateAndCounterOverflow(t *testing.T) {
	t.Run("stopped-certificate", func(t *testing.T) {
		s, now := fixture(t)
		n := newNode(t, s, "stopping")
		notificationExec(t, s, "INSERT INTO certificate_state(node_id,not_after,error,applied_error) VALUES(?,?,?,?)", n.ID, now.Add(time.Hour).Unix(), "failed", "failed")
		notificationExec(t, s, "INSERT INTO node_retirements(node_id,id,stopped,created_at) VALUES(?,?,1,?)", n.ID, NewID(), now.Unix())
		all := notificationCollect(t, s)
		for _, kind := range []string{"certificate.expiry", "certificate.issuance", "certificate.application"} {
			if len(notificationKinds(all, kind)) > 0 {
				t.Fatalf("stopped certificate remained eligible: %s", kind)
			}
		}
		if !all["certificate.application:"+n.ID].Removed {
			t.Fatal("certificate error missing deletion marker")
		}
	})
	t.Run("server-sum", func(t *testing.T) {
		s, now := fixture(t)
		_, a, b := serverPair(t, s)
		for _, n := range []model.Node{a, b} {
			notificationExec(t, s, "INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,?,?,?,0)", n.ID, "history", DayStart(*now), int64(1<<62))
		}
		if _, err := s.CollectNotificationConditions(); err == nil {
			t.Fatal("server total overflow was accepted")
		}
	})
	t.Run("node-sum", func(t *testing.T) {
		s, now := fixture(t)
		n := newNode(t, s, "overflow")
		notificationExec(t, s, "INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?)", n.ID, "history", DayStart(*now), int64(1<<62), int64(1<<62))
		if _, err := s.CollectNotificationConditions(); err == nil {
			t.Fatal("node total overflow was accepted")
		}
	})

}

func TestNotificationCollectorRetirementCancelsInsteadOfRecovery(t *testing.T) {
	s, now := fixture(t)
	v, a, _ := serverPair(t, s)
	if err := s.Report(a.ID, model.AgentReport{CoreHealthy: true, Metrics: model.Metrics{DiskTotal: 1 << 30, DiskUsed: (1 << 30) - (128 << 20)}}); err != nil {
		t.Fatal(err)
	}
	notificationExec(t, s, "INSERT INTO certificate_state(node_id,error) VALUES(?,?)", a.ID, "issuance failed")
	scan := func() {
		t.Helper()
		cs, err := s.CollectNotificationConditions()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.EvaluateAlerts(cs); err != nil {
			t.Fatal(err)
		}
	}
	scan()
	*now = now.Add(60 * time.Second)
	scan()
	notificationExec(t, s, "UPDATE nodes SET lifecycle='deleted',deleted_at=? WHERE json_extract(config,'$.serverId')=?", now.Unix(), v.ID)
	notificationExec(t, s, "UPDATE servers SET deleted_at=? WHERE id=?", now.Unix(), v.ID)
	scan()
	*now = now.Add(time.Minute)
	scan()
	items, err := s.ListAlerts(1, 100, "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range items.Items {
		if r.Status == "resolved" {
			t.Fatalf("retirement must not claim healthy: %+v", r)
		}
	}
}

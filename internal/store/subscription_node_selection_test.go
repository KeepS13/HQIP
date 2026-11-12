package store

import (
	"sort"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func automaticTestNode(t *testing.T, s *Store, name string) model.Node {
	t.Helper()
	n := newNode(t, s, name)
	if err := s.SetStatus(n.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	audienceExec(t, s, `UPDATE nodes SET capabilities='["admission-v1"]',last_seen=?,core_healthy=1 WHERE id=?`, s.Now().Unix(), n.ID)
	return n
}

func automaticTestQuota(t *testing.T, s *Store, node model.Node, quota int64) {
	t.Helper()
	audienceExec(t, s, "UPDATE servers SET config=json_set(config,'$.monthlyQuotaBytes',?) WHERE id=?", quota, node.ServerID)
}

func automaticTestBilling(t *testing.T, s *Store, node model.Node, start, expires time.Time) {
	t.Helper()
	audienceExec(t, s, "INSERT INTO server_billing(server_id,epoch,started_at,anchor,expires_at) VALUES(?,?,?,?,?)", node.ServerID, NewID(), start.Unix(), expires.Unix(), expires.Unix())
}

func requireAutomaticNode(t *testing.T, s *Store, ids []string, want string) {
	t.Helper()
	got, err := s.automaticSubscriptionNode(ids)
	if err != nil || got != want {
		t.Fatalf("automatic node = %q, want %q: %v", got, want, err)
	}
}

func TestAutomaticSubscriptionNodeCapacityExpiryAndEligibility(t *testing.T) {
	for _, scenario := range []string{"stable tie", "remaining bytes", "unlimited", "earliest expiry", "unknown expiry", "expired", "exhausted", "over quota", "stale", "future report", "unhealthy", "null report", "malformed report", "not ready", "retired", "incapable", "missing server", "deleted server", "cycle usage"} {
		t.Run(scenario, func(t *testing.T) {
			s, now := fixture(t)
			*now = time.Date(2026, 11, 15, 12, 0, 0, 0, Shanghai)
			a, b := automaticTestNode(t, s, "auto-a"), automaticTestNode(t, s, "auto-b")
			ids := []string{a.ID, b.ID}
			want := b.ID
			start := now.Add(-20 * 24 * time.Hour)
			switch scenario {
			case "stable tie":
				sort.Strings(ids)
				want = ids[0]
				ids = []string{ids[1], ids[0]}
			case "remaining bytes":
				automaticTestQuota(t, s, a, 1000)
				automaticTestQuota(t, s, b, 900)
				audienceExec(t, s, "INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?)", a.ID, "usage", DayStart(*now), 100, 101)
			case "unlimited":
				automaticTestQuota(t, s, a, 1000000000)
			case "earliest expiry":
				automaticTestBilling(t, s, a, start, now.Add(10*24*time.Hour))
				automaticTestBilling(t, s, b, start, now.Add(5*24*time.Hour))
			case "unknown expiry":
				automaticTestBilling(t, s, b, start, now.Add(5*24*time.Hour))
			case "expired":
				automaticTestBilling(t, s, a, start, *now)
			case "exhausted", "over quota":
				automaticTestQuota(t, s, a, 100)
				download := 40
				if scenario == "over quota" {
					download = 41
				}
				audienceExec(t, s, "INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?)", a.ID, "usage", DayStart(*now), 60, download)
			case "stale":
				audienceExec(t, s, "UPDATE nodes SET last_seen=? WHERE id=?", now.Add(-30*time.Second).Unix(), a.ID)
			case "future report":
				audienceExec(t, s, "UPDATE nodes SET last_seen=? WHERE id=?", now.Add(time.Second).Unix(), a.ID)
			case "unhealthy":
				audienceExec(t, s, "UPDATE nodes SET core_healthy=0 WHERE id=?", a.ID)
			case "null report":
				audienceExec(t, s, "UPDATE nodes SET online='null' WHERE id=?", a.ID)
			case "malformed report":
				audienceExec(t, s, `UPDATE nodes SET online='{"broken":"count"}' WHERE id=?`, a.ID)
			case "not ready":
				if err := s.SetStatus(a.ID, "pending_verification", "test", false); err != nil {
					t.Fatal(err)
				}
			case "retired":
				audienceExec(t, s, "UPDATE nodes SET lifecycle='retired' WHERE id=?", a.ID)
			case "incapable":
				audienceExec(t, s, "UPDATE nodes SET capabilities='[]' WHERE id=?", a.ID)
			case "missing server":
				audienceExec(t, s, "UPDATE nodes SET config=json_set(config,'$.serverId','missing') WHERE id=?", a.ID)
			case "deleted server":
				audienceExec(t, s, "UPDATE servers SET deleted_at=? WHERE id=?", now.Unix(), a.ServerID)
			case "cycle usage":
				automaticTestQuota(t, s, a, 1000)
				automaticTestQuota(t, s, b, 900)
				automaticTestBilling(t, s, a, time.Unix(DayStart(start), 0), now.Add(5*24*time.Hour))
				// Previous-month traffic still belongs to this billing cycle.
				audienceExec(t, s, "INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?)", a.ID, "usage", DayStart(start.Add(24*time.Hour)), 100, 101)
			}
			requireAutomaticNode(t, s, ids, want)
			requireAutomaticNode(t, s, []string{}, "")
			// Never escape the supplied subscription when its sole node is stale.
			audienceExec(t, s, "UPDATE nodes SET last_seen=0 WHERE id=?", b.ID)
			requireAutomaticNode(t, s, []string{b.ID}, "")
		})
	}
}

func TestAutomaticSubscriptionNodeUsesPeopleBeforeCapacity(t *testing.T) {
	s, _, user, template, a, _ := portalFixture(t, 3, nil)
	audienceExec(t, s, "UPDATE nodes SET last_seen=?,core_healthy=1 WHERE id=?", s.Now().Unix(), a.ID)
	b := automaticTestNode(t, s, "auto-other")
	copy := grantAnother(t, s, user.ID, template, "auto-copy")
	other, err := s.RegisterPortal("auto-other@example.com", "other-auto-password")
	if err != nil {
		t.Fatal(err)
	}
	otherSub := grantAnother(t, s, other.ID, template, "auto-other-user")
	for _, pair := range []struct{ node, identity, subscription string }{
		{a.ID, "a1", user.Subscriptions[0].ID}, {a.ID, "a2", copy.ID},
		{b.ID, "b1", user.Subscriptions[0].ID}, {b.ID, "b2", otherSub.ID},
	} {
		audienceExec(t, s, "INSERT INTO node_accounting_identities(node_id,accounting_id,subject_type,subject_id) VALUES(?,?,'subscription',?)", pair.node, pair.identity, pair.subscription)
	}
	audienceExec(t, s, "UPDATE nodes SET online=? WHERE id=?", jsonString(map[string]int{"a1": 8, "a2": 4, "legacy": 50, ProbeID(a.ID): 50}), a.ID)
	audienceExec(t, s, "UPDATE nodes SET online=? WHERE id=?", jsonString(map[string]int{"b1": 1, "b2": 1}), b.ID)
	automaticTestQuota(t, s, a, 10) // B is unlimited but has more distinct people.
	requireAutomaticNode(t, s, []string{b.ID, a.ID}, a.ID)
	// The same online person on both nodes ties, even with different connections.
	audienceExec(t, s, "UPDATE nodes SET online=? WHERE id=?", jsonString(map[string]int{"b1": 1}), b.ID)
	requireAutomaticNode(t, s, []string{a.ID, b.ID}, b.ID)
}

func TestAutomaticSubscriptionNodeSharesWholeServerCapacity(t *testing.T) {
	s, now := fixture(t)
	_, a, sibling := serverPair(t, s)
	if err := s.SetStatus(a.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	audienceExec(t, s, `UPDATE nodes SET capabilities='["admission-v1"]',last_seen=?,core_healthy=1 WHERE id=?`, now.Unix(), a.ID)
	b := automaticTestNode(t, s, "other-server")
	automaticTestQuota(t, s, a, 1000)
	automaticTestQuota(t, s, b, 800)
	// Traffic on a sibling outside the subscription consumes the same capacity.
	audienceExec(t, s, "INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?)", sibling.ID, "usage", DayStart(*now), 200, 100)
	audienceExec(t, s, "UPDATE nodes SET lifecycle='deleted' WHERE id=?", sibling.ID)
	requireAutomaticNode(t, s, []string{a.ID, b.ID}, b.ID)
	// Same-server candidates tie on capacity and expiry and then use node ID.
	audienceExec(t, s, "UPDATE nodes SET lifecycle='active' WHERE id=?", sibling.ID)
	if err := s.SetStatus(sibling.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	audienceExec(t, s, `UPDATE nodes SET capabilities='["admission-v1"]',last_seen=?,core_healthy=1 WHERE id=?`, now.Unix(), sibling.ID)
	ids := []string{a.ID, sibling.ID}
	sort.Strings(ids)
	requireAutomaticNode(t, s, ids, ids[0])
}

func TestSubscriptionTemplateIgnoresLegacyDefault(t *testing.T) {
	s, _, user, template, a, _ := portalFixture(t, 1, nil)
	if template.DefaultNodeID != "" {
		t.Fatal("legacy input was persisted")
	}
	b := automaticTestNode(t, s, "replacement")
	template.NodeIDs, template.DefaultNodeID = []string{b.ID}, a.ID
	if _, err := s.PreviewSubscriptionTemplate(template.ID, template); err != nil {
		t.Fatal("preview required a manual default", err)
	}
	saved, err := s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
	if err != nil || saved.DefaultNodeID != "" {
		t.Fatal("save required a manual default", err)
	}
	audienceExec(t, s, "UPDATE portal_templates SET config=json_set(config,'$.defaultNodeId',?) WHERE id=?", a.ID, template.ID)
	profile, sub, err := s.PortalClientProfile(user.ID, "subscription", user.Subscriptions[0].ID)
	if err != nil || profile.DefaultNodeID != b.ID || sub.DefaultNodeID != "" || len(profile.Nodes) != 1 {
		t.Fatal("legacy default affected profile", profile.DefaultNodeID, err)
	}
	audienceExec(t, s, "UPDATE nodes SET last_seen=0 WHERE id=?", b.ID)
	profile, _, err = s.PortalClientProfile(user.ID, "subscription", user.Subscriptions[0].ID)
	if err != nil || profile.DefaultNodeID != "" || len(profile.Nodes) != 1 {
		t.Fatal("empty automatic selection changed manual authorization", err)
	}
}

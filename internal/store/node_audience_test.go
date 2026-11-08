package store

import (
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func audienceExec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.DB.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func requireNodeAudience(t *testing.T, s *Store, id string, users, subscriptions int, onlineUsers, connections *int) {
	t.Helper()
	all, err := s.NodeAudienceCounts()
	if err != nil {
		t.Fatal(err)
	}
	v, ok := all[id]
	if !ok || v.SubscribedUsers != users || v.ActiveSubscriptions != subscriptions {
		t.Fatalf("membership want users=%d subs=%d: %+v (present=%v)", users, subscriptions, v, ok)
	}
	if onlineUsers == nil {
		if v.OnlineUsers != nil || v.OnlineConnections != nil {
			t.Fatalf("unknown online data was reported as a number: %+v", v)
		}
	} else if v.OnlineUsers == nil || v.OnlineConnections == nil || *v.OnlineUsers != *onlineUsers || *v.OnlineConnections != *connections {
		t.Fatalf("online want users=%d connections=%d: %+v", *onlineUsers, *connections, v)
	}
}

func audienceInt(v int) *int { return &v }

func TestNodeAudienceDeduplicatesPeopleAndUsesActualOnlineReports(t *testing.T) {
	s, now, user, template, node, password := portalFixture(t, 3, nil)
	second := grantAnother(t, s, user.ID, template, "second-copy")
	third := grantAnother(t, s, user.ID, template, "third-copy")
	other, err := s.RegisterPortal("other-audience@example.com", "other-test-password")
	if err != nil {
		t.Fatal(err)
	}
	otherSub := grantAnother(t, s, other.ID, template, "other-user")
	emptyNode := newNode(t, s, "empty-audience")
	requireNodeAudience(t, s, node.ID, 2, 4, nil, nil)
	requireNodeAudience(t, s, emptyNode.ID, 0, 0, nil, nil)

	a := acquire(t, s, node, password, "a")
	_ = acquire(t, s, node, password, "b")
	b := acquire(t, s, node, subscriptionPassword(t, s, user.ID, second.ID, node.ID), "c")
	c := acquire(t, s, node, subscriptionPassword(t, s, other.ID, otherSub.ID, node.ID), "d")
	// A held lease with no observed connection must not increase online counts.
	_ = acquire(t, s, node, subscriptionPassword(t, s, user.ID, third.ID, node.ID), "e")
	desired, err := s.Desired(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Report(node.ID, model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Online: map[string]int{a.AccountingID: 2, b.AccountingID: 1, c.AccountingID: 1}}); err != nil {
		t.Fatal(err)
	}
	// Even a legacy device or system sample present in stored data cannot
	// masquerade as an account user. Only mapped subscription identities count.
	audienceExec(t, s, "UPDATE nodes SET online=? WHERE id=?", jsonString(map[string]int{a.AccountingID: 2, b.AccountingID: 1, c.AccountingID: 1, ProbeID(node.ID): 99, "legacy-device": 10}), node.ID)
	requireNodeAudience(t, s, node.ID, 2, 4, audienceInt(2), audienceInt(4))
	listed, err := s.NodesWithAudience()
	if err != nil || len(listed) != 2 {
		t.Fatalf("node list failed: %d %v", len(listed), err)
	}
	for _, item := range listed {
		if item.ID == node.ID && (item.Audience.SubscribedUsers != 2 || item.Audience.OnlineUsers == nil || *item.Audience.OnlineUsers != 2) {
			t.Fatalf("list did not preserve audience: %+v", item.Audience)
		}
	}

	*now = now.Add(30 * time.Second)
	requireNodeAudience(t, s, node.ID, 2, 4, nil, nil)
	audienceExec(t, s, "UPDATE nodes SET last_seen=?,core_healthy=0 WHERE id=?", now.Unix(), node.ID)
	requireNodeAudience(t, s, node.ID, 2, 4, nil, nil)
	audienceExec(t, s, "UPDATE nodes SET last_seen=?,core_healthy=1,online='{}' WHERE id=?", now.Unix(), node.ID)
	requireNodeAudience(t, s, node.ID, 2, 4, audienceInt(0), audienceInt(0))
	audienceExec(t, s, "UPDATE nodes SET online='null' WHERE id=?", node.ID)
	requireNodeAudience(t, s, node.ID, 2, 4, nil, nil)
}

func TestNodeAudienceMembershipMatchesSubscriptionEligibility(t *testing.T) {
	quota := 4 * model.MinimumSubscriptionLeaseBytes
	s, now, user, template, node, _ := portalFixture(t, 3, &quota)
	revoked := grantAnother(t, s, user.ID, template, "revoked-copy")
	expired := grantAnother(t, s, user.ID, template, "expired-copy")
	exhausted := grantAnother(t, s, user.ID, template, "exhausted")
	dust := grantAnother(t, s, user.ID, template, "unused-tail")
	liveTail := grantAnother(t, s, user.ID, template, "live-tail")
	revokedTail := grantAnother(t, s, user.ID, template, "revoked-tail")
	liveLease := acquire(t, s, node, subscriptionPassword(t, s, user.ID, liveTail.ID, node.ID), "l")
	revokedLease := acquire(t, s, node, subscriptionPassword(t, s, user.ID, revokedTail.ID, node.ID), "r")
	audienceExec(t, s, "UPDATE portal_subscriptions SET revoked=1 WHERE id=?", revoked.ID)
	audienceExec(t, s, "UPDATE portal_subscriptions SET expires=? WHERE id=?", now.Unix(), expired.ID)
	audienceExec(t, s, "UPDATE portal_usage SET upload=? WHERE subscription_id=?", quota, exhausted.ID)
	for _, subscription := range []model.UserSubscription{dust, liveTail, revokedTail} {
		audienceExec(t, s, "UPDATE portal_usage SET upload=?,reserved=? WHERE subscription_id=?", quota-478, quota, subscription.ID)
	}
	audienceExec(t, s, "UPDATE portal_leases SET revoked=1 WHERE id=?", revokedLease.ID)
	for _, check := range []struct {
		id     string
		status string
	}{{user.Subscriptions[0].ID, "active"}, {revoked.ID, "revoked"}, {expired.ID, "expired"}, {exhausted.ID, "exhausted"}, {dust.ID, "exhausted"}, {liveTail.ID, "active"}, {revokedTail.ID, "exhausted"}} {
		value, err := s.UserSubscription(user.ID, check.id)
		if err != nil || value.Status != check.status {
			t.Fatalf("eligibility fixture %s=%+v expected=%s err=%v", check.id, value, check.status, err)
		}
	}
	requireNodeAudience(t, s, node.ID, 1, 2, nil, nil)

	// A disabled account or template cannot contribute a current subscriber.
	other, err := s.RegisterPortal("disabled-audience@example.com", "another-test-password")
	if err != nil {
		t.Fatal(err)
	}
	grantAnother(t, s, other.ID, template, "disabled-user")
	audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", other.ID)
	unused := template
	unused.ID = ""
	unused.Name = "disabled template"
	unused, err = s.SaveSubscriptionTemplate("", unused, 0)
	if err != nil {
		t.Fatal(err)
	}
	grantAnother(t, s, user.ID, unused, "disabled-template")
	unused.Enabled = false
	if _, err = s.SaveSubscriptionTemplate(unused.ID, unused, unused.Revision); err != nil {
		t.Fatal(err)
	}
	requireNodeAudience(t, s, node.ID, 1, 2, nil, nil)
	// Expiring the only live tail makes it exhausted without counting its held
	// reservation as usage or permission to start another connection.
	audienceExec(t, s, "UPDATE portal_leases SET expires=? WHERE id=?", now.Unix(), liveLease.ID)
	requireNodeAudience(t, s, node.ID, 1, 1, nil, nil)
}

package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

// Exercise production node-quota reports with fractional billing and prepaid
// renewal periods. Removed-node settlement must survive the allocation change,
// and the replacement node must inherit the subscription's remaining quota.
func TestSubscriptionNodesContinueBillingAcrossReplacement(t *testing.T) {
	s, _, user, template, sub, password := renewalFixture(t)
	paidRenewal(t, s, template, sub, "node-replacement-renewal")
	oldNode := sub.NodeIDs[0]
	newNode := automaticTestNode(t, s, "replacement-billing")
	enableBilling(t, s, 150, 150)
	for _, node := range []string{oldNode, newNode.ID} {
		if err := s.SetOfflineAdmissionEnabled(node, true); err != nil {
			t.Fatal(err)
		}
	}
	old, err := s.AcquireNodeQuotaGrant(oldNode, strings.Repeat("a", 32), password, "hysteria2", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	report := func(g model.OfflineGrant, version, upload int64, closed bool) model.OfflineGrant {
		t.Helper()
		ack, err := s.ReportOfflineGrant(g.NodeID, g.ID, model.OfflineUsage{
			Version: version, UploadBytes: upload, Active: !closed, Closed: closed,
			AppliedRateVersion: g.RateVersion,
			Billing:            []billing.Segment{{Version: g.Billing.Version, Upload: upload, AppliedAt: g.AuthorizedAt}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return ack
	}
	old = report(old, 1, 1, false)
	before, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || before.UsedBytes != 1 || len(before.Periods) != 2 {
		t.Fatal("missing initial usage or renewal", before, err)
	}
	updated, err := s.UpdateSubscriptionNodes(user.ID, sub.ID, []string{newNode.ID}, before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	expected := *before
	expected.NodeIDs, expected.Revision = updated.NodeIDs, before.Revision+1
	if !reflect.DeepEqual(updated, expected) {
		t.Fatalf("replacement changed billing/entitlement: %+v vs %+v", updated, expected)
	}
	if _, err = s.AcquireNodeQuotaGrant(oldNode, strings.Repeat("b", 32), password, "hysteria2", "192.0.2.2"); !errors.Is(err, ErrEntitlement) {
		t.Fatal("removed node still admits connections", err)
	}
	// The old node reports more traffic after replacement. Replaying the same
	// cumulative report must neither drop these bytes nor charge them twice.
	closed := report(old, 2, 3, true)
	replay := report(old, 2, 3, true)
	if !closed.Closed || closed.NodeQuota.UsedUnits != 450 || replay.NodeQuota.Revision != closed.NodeQuota.Revision {
		t.Fatal("late settlement or replay changed the ledger", closed, replay)
	}
	newPassword, err := s.PortalCredential(user.ID, sub.ID, newNode.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.AcquireNodeQuotaGrant(newNode.ID, strings.Repeat("c", 32), newPassword, "vless", "192.0.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.NodeQuota.PeriodID != before.CurrentPeriodID || fresh.NodeQuota.UsedUnits != 450 || fresh.NodeQuota.NodeUsedUnits != 0 || *fresh.NodeQuota.QuotaUnits != *before.QuotaBytes*100 {
		t.Fatalf("replacement received a reset quota: %+v", fresh.NodeQuota)
	}
	settled := report(fresh, 1, 5, true)
	replay = report(fresh, 1, 5, true)
	if settled.NodeQuota.UsedUnits != 1200 || settled.NodeQuota.NodeUsedUnits != 750 || replay.NodeQuota.Revision != settled.NodeQuota.Revision {
		t.Fatal("new node usage did not accumulate exactly once", settled, replay)
	}
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || after.UsedBytes != 12 || after.ReservedBytes != 0 || after.Occupied != 0 || after.Periods[0].UploadBytes != 12 {
		t.Fatal("new and old node traffic failed to reconcile", after, err)
	}
	if !reflect.DeepEqual(after.Periods[1], before.Periods[1]) || !after.ExpiresAt.Equal(before.ExpiresAt) || !after.CurrentPeriodEndsAt.Equal(before.CurrentPeriodEndsAt) {
		t.Fatal("replacement changed prepaid period or expiry", after)
	}
}

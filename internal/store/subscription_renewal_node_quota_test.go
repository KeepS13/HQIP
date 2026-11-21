package store

import (
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

// Exercise the production node-quota entry point, including a delayed report
// after rollover. Legacy reservation tests alone cannot catch these regressions.
func TestRenewalNodeQuotaIsolatesPeriodsAndAuthorization(t *testing.T) {
	s, now, user, template, sub, password := renewalFixture(t)
	node := sub.NodeIDs[0]
	enableBilling(t, s, 150, 150)
	if err := s.SetOfflineAdmissionEnabled(node, true); err != nil {
		t.Fatal(err)
	}
	*now = sub.ExpiresAt.Add(-time.Hour)
	old, err := s.AcquireNodeQuotaGrant(node, strings.Repeat("q", 32), password, "hysteria2", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	paidRenewal(t, s, template, sub, "node-quota-renewal")
	report := func(g model.OfflineGrant, version, upload int64, closed bool) model.OfflineGrant {
		t.Helper()
		ack, err := s.ReportOfflineGrant(node, g.ID, model.OfflineUsage{
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
	if !old.OfflineUntil.Equal(sub.ExpiresAt) || old.NodeQuota.PeriodID != sub.ID || old.NodeQuota.UsedUnits != 150 {
		t.Fatalf("renewal crossed authorization boundary or lost fractional usage: %+v", old)
	}
	*now = sub.ExpiresAt
	fresh, err := s.AcquireNodeQuotaGrant(node, strings.Repeat("r", 32), password, "vless", "192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.NodeQuota.PeriodID == sub.ID || fresh.NodeQuota.UsedUnits != 0 || fresh.NodeQuota.NodeUsedUnits != 0 {
		t.Fatalf("new period inherited old watermark: %+v", fresh.NodeQuota)
	}
	late := report(old, 2, 2, true)
	if late.NodeQuota.PeriodID != sub.ID {
		t.Fatal("late receipt relabeled old children with the new period")
	}
	fresh = report(fresh, 1, 1, true)
	if fresh.NodeQuota.UsedUnits != 150 || fresh.NodeQuota.NodeUsedUnits != 150 {
		t.Fatalf("old report contaminated current quota: %+v", fresh.NodeQuota)
	}
	current, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || current.UsedBytes != 1 || current.ReservedBytes != 0 || current.Periods[0].UploadBytes != 3 {
		t.Fatal("period ledgers do not reconcile", current, err)
	}
}

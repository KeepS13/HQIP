package store

import (
	"errors"
	"fmt"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
	"strings"
	"sync"
	"testing"
	"time"
)

func enableBilling(t *testing.T, store *Store, hy2, vless int64) {
	if _, err := store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1","traffic-billing-v1"]'`); err != nil {
		t.Fatal(err)
	}
	t.Helper()
	settings, err := store.TrafficBillingSettings()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveTrafficBillingSettings(model.TrafficBillingInput{Enabled: true, ExpectedRevision: settings.Revision, Hysteria2: hy2, VLESS: vless}, "test-admin")
	if err != nil {
		t.Fatal(err)
	}
}

func TestTrafficBillingFactorsBothDirectionsAndLegacy(t *testing.T) {
	for _, factor := range []int64{1, 100, 150, 1000} {
		t.Run(fmt.Sprint(factor), func(t *testing.T) {
			quota := int64(1000000)
			store, now, user, _, node, password := portalFixture(t, 1, &quota)
			legacy, err := store.AcquireLease(node.ID, strings.Repeat("b", 32), password)
			if err != nil {
				t.Fatal(err)
			}
			if legacy.Billing != nil {
				t.Fatal("legacy lease unexpectedly repriced")
			}
			enableBilling(t, store, factor, factor)
			if _, err = store.RenewLease(node.ID, legacy.ID, model.LeaseUsage{UploadBytes: 1000, Closed: true}); err != nil {
				t.Fatal(err)
			}
			lease, err := store.AcquireProtocolLease(node.ID, strings.Repeat("a", 32), password, "hysteria2", false)
			if err != nil {
				t.Fatal(err)
			}
			const raw = 12345 + 6789
			usage := model.LeaseUsage{UploadBytes: 12345, DownloadBytes: 6789, Closed: true, Billing: []billing.Segment{{Version: lease.Billing.Version, Upload: 12345, Download: 6789, AppliedAt: *now}}}
			if _, err = store.RenewLease(node.ID, lease.ID, usage); err != nil {
				t.Fatal(err)
			}
			sub, _ := store.UserSubscription(user.ID, user.Subscriptions[0].ID)
			if sub.UsedBytes != raw*factor/100+1000 || sub.ReservedBytes != 0 {
				t.Fatalf("factor=%d actual=%+v", factor, sub)
			}
			var observed int64
			store.DB.QueryRow("SELECT upload+download FROM portal_leases WHERE id=?", lease.ID).Scan(&observed)
			if observed != raw {
				t.Fatal("administrator raw counters scaled")
			}
			if _, err = store.AcquireLease(node.ID, strings.Repeat("c", 32), password); !errors.Is(err, ErrEntitlement) {
				t.Fatal("unsupported new runtime bypassed enabled multiplier", err)
			}
			sub, _ = store.UserSubscription(user.ID, user.Subscriptions[0].ID)
			if sub.UsedBytes != raw*factor/100+1000 {
				t.Fatal("legacy compatibility changed")
			}
		})
	}
}

func TestTrafficBillingFractionDoesNotDependOnDirectionOrBatch(t *testing.T) {
	quota := int64(64 << 20)
	store, now, user, _, node, password := portalFixture(t, 2, &quota)
	enableBilling(t, store, 50, 50)
	leases := make([]model.ConnectionLease, 2)
	for index := range leases {
		var err error
		leases[index], err = store.AcquireProtocolLease(node.ID, strings.Repeat(string(rune('a'+index)), 32), password, []string{"hysteria2", "vless"}[index], false)
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for index := range leases {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			l := leases[index]
			for count := int64(1); count <= 101; count++ {
				usage := model.LeaseUsage{UploadBytes: count, DownloadBytes: count, Closed: count == 101, Billing: []billing.Segment{{Version: l.Billing.Version, Upload: count, Download: count, AppliedAt: *now}}}
				if _, err := store.RenewLease(node.ID, l.ID, usage); err != nil {
					t.Error(err)
					return
				}
			}
		}(index)
	}
	wg.Wait()
	sub, _ := store.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if sub.UsedBytes != 202 || sub.ReservedBytes != 0 {
		t.Fatalf("concurrent fractional usage %+v", sub)
	}
}

func TestTrafficBillingConfirmationScopeAndExpiry(t *testing.T) {
	quota := int64(1000000)
	store, now, user, _, node, password := portalFixture(t, 1, &quota)
	enableBilling(t, store, 150, 150)
	l, err := store.AcquireProtocolLease(node.ID, strings.Repeat("a", 32), password, "hysteria2", false)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := store.BillingDisplay(user.ID, l.SessionID, node.ID, "hysteria2")
	if err != nil || len(policies) != 0 {
		t.Fatal("issued policy exposed before runtime confirmation")
	}
	segment := billing.Segment{Version: l.Billing.Version, Upload: 100, AppliedAt: *now, ValidUntil: l.ExpiresAt}
	if _, err = store.RenewLease(node.ID, l.ID, model.LeaseUsage{UploadBytes: 100, Billing: []billing.Segment{segment}}); err != nil {
		t.Fatal(err)
	}
	enableBilling(t, store, 1000, 1)
	policies, err = store.BillingDisplay(user.ID, l.SessionID, node.ID, "hysteria2")
	if err != nil || len(policies) != 1 || policies[0].Multiplier != 150 || !policies[0].ValidUntil.Equal(l.ExpiresAt) {
		t.Fatal("unconfirmed policy or expiry leaked", policies, err)
	}
	for _, scope := range [][4]string{{"another", l.SessionID, node.ID, "hysteria2"}, {user.ID, "another", node.ID, "hysteria2"}, {user.ID, l.SessionID, node.ID, "vless"}} {
		values, err := store.BillingDisplay(scope[0], scope[1], scope[2], scope[3])
		if err != nil || len(values) > 0 {
			t.Fatal("billing scope isolation failed")
		}
	}
	*now = now.Add(3 * time.Minute)
	sub, _ := store.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if sub.ReservedBytes == 0 {
		t.Fatal("unconfirmed budget released on expiry")
	}
	segment.Upload = 200
	final := model.LeaseUsage{UploadBytes: 200, Closed: true, Billing: []billing.Segment{segment}}
	if _, err = store.RenewLease(node.ID, l.ID, final); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RenewLease(node.ID, l.ID, final); err != nil {
		t.Fatal(err)
	}
	sub, _ = store.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if sub.UsedBytes != 300 || sub.ReservedBytes != 0 {
		t.Fatal("late final repriced or leaked reservation", sub)
	}
	final.Billing[0].Version++
	if _, err = store.RenewLease(node.ID, l.ID, final); err == nil {
		t.Fatal("mutated final accepted")
	}
}

func TestTrafficBillingCapabilityGate(t *testing.T) {
	quota := int64(1000000)
	store, _, _, _, _, _ := portalFixture(t, 1, &quota)
	settings, _ := store.TrafficBillingSettings()
	_, err := store.SaveTrafficBillingSettings(model.TrafficBillingInput{Enabled: true, ExpectedRevision: settings.Revision, Hysteria2: 150, VLESS: 150}, "admin")
	if !errors.Is(err, ErrConflict) {
		t.Fatal("enabled on an unsupported runtime", err)
	}
}

func TestTrafficBillingTailAdmissionUsesBilledMinimum(t *testing.T) {
	for _, factor := range []int64{1, 100, 150, 1000} {
		t.Run(fmt.Sprint(factor), func(t *testing.T) {
			quota := int64(1000000)
			store, _, user, _, node, password := portalFixture(t, 1, &quota)
			enableBilling(t, store, factor, factor)
			minimum := billing.Ceiling(model.MinimumSubscriptionLeaseBytes * factor)
			if _, err := store.DB.Exec("UPDATE portal_usage SET upload=? WHERE subscription_id=?", quota-minimum+1, user.Subscriptions[0].ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AcquireProtocolLease(node.ID, strings.Repeat("a", 32), password, "hysteria2", false); !errors.Is(err, ErrEntitlement) {
				t.Fatal("sub-minimum tail admitted", err)
			}
			if _, err := store.DB.Exec("UPDATE portal_usage SET upload=? WHERE subscription_id=?", quota-minimum, user.Subscriptions[0].ID); err != nil {
				t.Fatal(err)
			}
			lease, err := store.AcquireProtocolLease(node.ID, strings.Repeat("b", 32), password, "hysteria2", false)
			if err != nil {
				t.Fatal(err)
			}
			if lease.Billing.BudgetUnits/factor < model.MinimumSubscriptionLeaseBytes {
				t.Fatal("converted forwarding budget below minimum")
			}
		})
	}
}

func TestTrafficBillingUnlimitedStillAccumulates(t *testing.T) {
	store, now, node, password := offlineFixture(t, 1, nil)
	enableBilling(t, store, 150, 150)
	grant, err := store.AcquireProtocolOfflineGrant(node.ID, strings.Repeat("a", 32), password, "hysteria2")
	if err != nil {
		t.Fatal(err)
	}
	usage := model.OfflineUsage{Version: 1, UploadBytes: 100, DownloadBytes: 100, Closed: true, Billing: []billing.Segment{{Version: grant.Billing.Version, Upload: 100, Download: 100, AppliedAt: *now}}}
	if _, err = store.ReportOfflineGrant(node.ID, grant.ID, usage); err != nil {
		t.Fatal(err)
	}
	var used, reserved int64
	err = store.DB.QueryRow("SELECT upload+download,reserved FROM portal_usage WHERE subscription_id=?", grant.SubscriptionID).Scan(&used, &reserved)
	if err != nil || used != 300 || reserved != 0 {
		t.Fatal("unlimited subscription lost charges", used, reserved, err)
	}
}

func TestTrafficBillingProtocolRemainderAndReplay(t *testing.T) {
	quota := int64(1000000)
	store, now, user, _, node, password := portalFixture(t, 2, &quota)
	enableBilling(t, store, 150, 200)
	for index, protocol := range []string{"hysteria2", "vless"} {
		lease, err := store.AcquireProtocolLease(node.ID, strings.Repeat(string(rune('a'+index)), 32), password, protocol, false)
		if err != nil {
			t.Fatal(err)
		}
		segment := billing.Segment{Version: lease.Billing.Version, Upload: 1, AppliedAt: *now}
		usage := model.LeaseUsage{UploadBytes: 1, Closed: true, Billing: []billing.Segment{segment}}
		if _, err = store.RenewLease(node.ID, lease.ID, usage); err != nil {
			t.Fatal(err)
		}
		if _, err = store.RenewLease(node.ID, lease.ID, usage); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := store.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.UsedBytes != 3 || sub.ReservedBytes != 0 {
		t.Fatalf("bad accounting %+v", sub)
	}
	lease, err := store.AcquireProtocolLease(node.ID, strings.Repeat("c", 32), password, "hysteria2", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RenewLease(node.ID, lease.ID, model.LeaseUsage{UploadBytes: 1, Closed: true, Billing: []billing.Segment{{Version: lease.Billing.Version, Upload: 1, AppliedAt: *now}}}); err != nil {
		t.Fatal(err)
	}
	sub, _ = store.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if sub.UsedBytes != 5 || sub.ReservedBytes != 0 {
		t.Fatalf("fraction lost across leases: %+v", sub)
	}
}

func TestTrafficBillingHotChangeDoesNotRepriceOrDoubleCharge(t *testing.T) {
	quota := int64(10000000)
	store, now, user, _, node, password := portalFixture(t, 1, &quota)
	enableBilling(t, store, 150, 150)
	lease, err := store.AcquireProtocolLease(node.ID, strings.Repeat("a", 32), password, "hysteria2", false)
	if err != nil {
		t.Fatal(err)
	}
	old := billing.Segment{Version: lease.Billing.Version, Upload: 1000, AppliedAt: *now}
	usage := model.LeaseUsage{UploadBytes: 1000, Billing: []billing.Segment{old}}
	if _, err = store.RenewLease(node.ID, lease.ID, usage); err != nil {
		t.Fatal(err)
	}
	enableBilling(t, store, 250, 150)
	issued, err := store.RenewLease(node.ID, lease.ID, usage)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Billing.Multiplier != 250 {
		t.Fatal("new policy not issued")
	}
	old.Upload = 1200
	current := billing.Segment{Version: issued.Billing.Version, Upload: 800, AppliedAt: now.Add(time.Second)}
	usage = model.LeaseUsage{UploadBytes: 2000, Closed: true, Billing: []billing.Segment{old, current}}
	if _, err = store.RenewLease(node.ID, lease.ID, usage); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RenewLease(node.ID, lease.ID, usage); err != nil {
		t.Fatal(err)
	}
	sub, _ := store.UserSubscription(user.ID, user.Subscriptions[0].ID)
	if sub.UsedBytes != 3800 || sub.ReservedBytes != 0 {
		t.Fatalf("reprice or replay: %+v", sub)
	}
}

func TestTrafficBillingSettingsValidationAndCAS(t *testing.T) {
	store, _ := fixture(t)
	settings, err := store.TrafficBillingSettings()
	if err != nil || settings.Hysteria2 != 150 || settings.VLESS != 150 || settings.Enabled {
		t.Fatalf("migration defaults %+v %v", settings, err)
	}
	for _, factor := range []int64{0, -1, 1001} {
		if _, err = store.SaveTrafficBillingSettings(model.TrafficBillingInput{ExpectedRevision: settings.Revision, Hysteria2: factor, VLESS: 150}, "admin"); err == nil {
			t.Fatal("invalid factor accepted")
		}
	}
	enableBilling(t, store, 1, 1000)
	_, err = store.SaveTrafficBillingSettings(model.TrafficBillingInput{ExpectedRevision: settings.Revision, Hysteria2: 100, VLESS: 100}, "admin")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent overwrite accepted: %v", err)
	}
}

func TestTrafficBillingOfflineSettlementAndEmergency(t *testing.T) {
	quota := int64(1000000)
	store, now, node, password := offlineFixture(t, 1, &quota)
	enableBilling(t, store, 150, 200)
	grant, err := store.AcquireProtocolOfflineGrant(node.ID, strings.Repeat("a", 32), password, "hysteria2")
	if err != nil {
		t.Fatal(err)
	}
	segment := billing.Segment{Version: grant.Billing.Version, Upload: 200, EmergencyUpload: 100, AppliedAt: *now}
	usage := model.OfflineUsage{Version: 1, UploadBytes: 200, EmergencyUsedBytes: 100, Closed: true, Billing: []billing.Segment{segment}}
	if _, err = store.ReportOfflineGrant(node.ID, grant.ID, usage); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReportOfflineGrant(node.ID, grant.ID, usage); err != nil {
		t.Fatal(err)
	}
	var used, reserved int64
	if err = store.DB.QueryRow("SELECT upload+download,reserved FROM portal_usage WHERE subscription_id=?", grant.SubscriptionID).Scan(&used, &reserved); err != nil {
		t.Fatal(err)
	}
	if used != 300 || reserved != 0 {
		t.Fatalf("offline: used=%d reserved=%d", used, reserved)
	}
}

func TestTrafficBillingRejectsForgedAndOverBudgetSegments(t *testing.T) {
	quota := int64(1000000)
	store, now, _, _, node, password := portalFixture(t, 1, &quota)
	enableBilling(t, store, 1000, 1)
	lease, err := store.AcquireProtocolLease(node.ID, strings.Repeat("a", 32), password, "vless", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range []billing.Segment{{Version: lease.Billing.Version + 1, Upload: 1, AppliedAt: *now}, {Version: lease.Billing.Version, Upload: lease.Billing.BudgetUnits + 1, AppliedAt: *now}} {
		if _, err = store.RenewLease(node.ID, lease.ID, model.LeaseUsage{UploadBytes: segment.Upload, Billing: []billing.Segment{segment}}); err == nil {
			t.Fatal("invalid usage accepted")
		}
	}
}

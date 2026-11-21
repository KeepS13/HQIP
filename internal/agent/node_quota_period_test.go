package agent

import "testing"

func TestNodeQuotaRenewalIgnoresOldPeriodAllocationsAfterRestart(t *testing.T) {
	a := testAgent(t, "", "http://127.0.0.1:1")
	now := a.now()
	old := quotaTestState(now, "old", "hysteria2", 150_000)
	// An old acknowledgement can arrive after a newer period has started.
	old.Grant.NodeQuota.Revision = 100
	old.Grant.NodeQuota.PeriodID = old.Grant.SubscriptionID
	old.Children["pending"] = offlineChild{NormalUsed: 50_000, Normal: 150_000}
	fresh := quotaTestState(now, "fresh", "vless", 150_000)
	fresh.Grant.NodeQuota.PeriodID = "renewed-period"
	for _, state := range []offlineState{old, fresh} {
		if err := a.ledger.saveOffline(state); err != nil {
			t.Fatal(err)
		}
	}
	path := a.cfg.StatePath
	if err := a.ledger.close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.localOfflineGrant(&fresh, "new-child", now)
	if err != nil || child.Billing.BudgetUnits != 150_000 {
		t.Fatal("old usage or unknown allocations consumed renewed quota", child, err)
	}
}

func TestNodeQuotaInitialPeriodIncludesLegacySnapshots(t *testing.T) {
	a := testAgent(t, "", "http://127.0.0.1:1")
	now := a.now()
	legacy := quotaTestState(now, "legacy", "hysteria2", 150_000)
	legacy.Children["pending"] = offlineChild{NormalUsed: 50_000, Normal: 70_000}
	current := quotaTestState(now, "current", "vless", 150_000)
	current.Grant.NodeQuota.PeriodID = current.Grant.SubscriptionID
	current.Grant.NodeQuota.Revision = 2
	current.Grant.NodeQuota.NodeUsedUnits = 50_000
	current.Grant.NodeQuota.UsedUnits = 50_000
	remaining, err := nodeQuotaAvailable([]offlineState{legacy, current}, current.Grant.SubscriptionID, current.Grant.NodeQuota.PeriodID)
	if err != nil || remaining != 80_000 {
		t.Fatal("upgrade lost an initial-period delegation", remaining, err)
	}
}

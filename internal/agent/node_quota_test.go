package agent

import (
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

func quotaTestState(now time.Time, id, protocol string, units int64) offlineState {
	g := offlineTestGrant(now)
	g.ID = id
	g.NormalBudgetBytes = 0
	g.EmergencyBudgetBytes = 0
	g.OfflineUntil = now.Add(72 * time.Hour)
	g.NodeQuota = &model.NodeQuotaSnapshot{Revision: 1, QuotaUnits: &units}
	g.Billing = &billing.Grant{Policy: billing.Policy{Version: 1, Protocol: protocol, Multiplier: 150}, BudgetUnits: billing.MaxRawBytes}
	return offlineState{Grant: g, Children: map[string]offlineChild{}, LastWall: now}
}

func TestNodeQuotaSharesProtocolsAndSurvivesRestart(t *testing.T) {
	a := testAgent(t, "", "http://127.0.0.1:1")
	now := a.now()
	hy := quotaTestState(now, "hy-parent", "hysteria2", 150_000)
	vl := quotaTestState(now, "vl-parent", "vless", 150_000)
	for _, s := range []offlineState{hy, vl} {
		if err := a.ledger.saveOffline(s); err != nil {
			t.Fatal(err)
		}
	}
	child, err := a.localOfflineGrant(&hy, "hy-child", now)
	if err != nil || child.Billing.BudgetUnits != 150_000 {
		t.Fatal("full shared quota", child, err)
	}
	if err = a.ledger.saveOffline(hy); err != nil {
		t.Fatal(err)
	}
	if _, err = a.localOfflineGrant(&vl, "vl-child", now); err == nil {
		t.Fatal("second protocol duplicated quota")
	}
	usage := model.LeaseUsage{DownloadBytes: 400, Closed: true, Billing: []billing.Segment{{Version: 1, Download: 400, AppliedAt: now}}}
	if err = hy.updateChild("hy-child", usage, now); err != nil {
		t.Fatal(err)
	}
	if err = a.ledger.saveOffline(hy); err != nil {
		t.Fatal(err)
	}
	path := a.cfg.StatePath
	if err = a.ledger.close(); err != nil {
		t.Fatal(err)
	}
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	// Beyond the old 24-hour website grace, the known subscription remains
	// valid. Only the 600 raw bytes left at multiplier 1.50 may be delegated.
	child, err = a.localOfflineGrant(&vl, "vl-child", now.Add(25*time.Hour))
	if err != nil || child.Billing.BudgetUnits != 90_000 {
		t.Fatal("restart reset quota or imposed website deadline", child, err)
	}
	if err = a.ledger.saveOffline(vl); err != nil {
		t.Fatal(err)
	}
	usage = model.LeaseUsage{DownloadBytes: 600, Closed: true, Billing: []billing.Segment{{Version: 1, Download: 600, AppliedAt: now.Add(25 * time.Hour)}}}
	if err = vl.updateChild("vl-child", usage, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = a.ledger.saveOffline(vl); err != nil {
		t.Fatal(err)
	}
	if _, err = a.localOfflineGrant(&hy, "next", now.Add(25*time.Hour)); err == nil {
		t.Fatal("exhausted shared node quota admitted")
	}
}

func TestNodeQuotaWatermarkDoesNotDoubleCountReports(t *testing.T) {
	now := time.Now()
	a := quotaTestState(now, "a", "hysteria2", 200_000)
	a.Children["a"] = offlineChild{NormalUsed: 50_000, Normal: 70_000}
	a.Grant.NodeQuota = &model.NodeQuotaSnapshot{Revision: 2, QuotaUnits: a.Grant.NodeQuota.QuotaUnits, UsedUnits: 80_000, NodeUsedUnits: 50_000, LegacyReservedUnits: 10_000}
	b := quotaTestState(now, "b", "vless", 200_000)
	b.Children["b"] = offlineChild{NormalUsed: 10_000, Normal: 30_000, Ended: true}
	available, err := nodeQuotaAvailable([]offlineState{a, b}, "s", "")
	if err != nil || available != 60_000 {
		t.Fatal("watermark or unknown delegated bytes reused", available, err)
	}
	a.Grant.NodeQuota.NodeUsedUnits = 70_000
	if _, err = nodeQuotaAvailable([]offlineState{a, b}, "s", ""); err == nil {
		t.Fatal("missing local usage was guessed")
	}
}

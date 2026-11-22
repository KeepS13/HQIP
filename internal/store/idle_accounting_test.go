package store

import (
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/apernet/hysteria/core/v2/billing"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestIdleAccountingDoesNotRewriteUsageAndQuotaCounters(t *testing.T) {
	quota := int64(1_000_000)
	s, now, node, password := offlineFixture(t, 2, &quota)
	enableBilling(t, s, 150, 150)
	g, err := s.legacyNodeQuotaGrant(node.ID, strings.Repeat("i", 64), password, "hysteria2")
	if err != nil {
		t.Fatal(err)
	}
	u := model.OfflineUsage{Version: 1, Active: true, DownloadBytes: 1, AppliedRateVersion: g.RateVersion,
		Billing: []billing.Segment{{Version: g.Billing.Version, Download: 1, AppliedAt: *now}}}
	if g, err = s.ReportOfflineGrant(node.ID, g.ID, u); err != nil {
		t.Fatal(err)
	}
	db := s.DB
	if s.Postgres() {
		config, err := pgstore.Config(s.postgresDSN)
		if err != nil {
			t.Fatal(err)
		}
		db = stdlib.OpenDB(*config)
		defer db.Close()
	}
	if _, err = db.Exec("CREATE TABLE idle_accounting_writes(kind TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"subscription_periods", "portal_usage", "metadata"} {
		condition := ""
		if table == "metadata" {
			condition = " WHEN (NEW.key LIKE 'node-quota-used:%')"
		}
		trigger := "CREATE TRIGGER count_idle_" + table + " AFTER UPDATE ON " + table + condition + " BEGIN INSERT INTO idle_accounting_writes(kind) VALUES('" + table + "'); END"
		if s.Postgres() {
			function := "count_idle_" + table
			if _, err = db.Exec("CREATE FUNCTION " + function + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO idle_accounting_writes(kind) VALUES('" + table + "'); RETURN NEW; END $$"); err != nil {
				t.Fatal(err)
			}
			trigger = "CREATE TRIGGER " + function + " AFTER UPDATE ON " + table + " FOR EACH ROW" + condition + " EXECUTE FUNCTION " + function + "()"
		}
		if _, err = db.Exec(trigger); err != nil {
			t.Fatal(err)
		}
	}
	u.Version, u.AppliedRateVersion = 2, g.RateVersion
	if g, err = s.ReportOfflineGrant(node.ID, g.ID, u); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM idle_accounting_writes").Scan(&count); err != nil || count != 0 {
		t.Fatal("zero increment rewrote accounting rows", count, err)
	}
	// Actual new bytes still settle, including the retained half-byte remainder.
	u.Version, u.DownloadBytes, u.AppliedRateVersion = 3, 5, g.RateVersion
	u.Billing[0].Download = 5
	if g, err = s.ReportOfflineGrant(node.ID, g.ID, u); err != nil {
		t.Fatal(err)
	}
	e, err := s.UserSubscription(g.UserID, g.SubscriptionID)
	if err != nil || e.UsedBytes != 7 {
		t.Fatal("zero-write optimization changed fractional billing", e, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM idle_accounting_writes").Scan(&count); err != nil || count != 3 {
		t.Fatal("real usage did not update both ledgers and node counter", count, err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = updatePeriodUsage(tx, "wrong-subscription", e.CurrentPeriodID, 0, 0, 0); err == nil {
		t.Fatal("zero increment bypassed period ownership validation")
	}
	if err = updatePeriodUsage(tx, e.ID, "missing-period", 0, 0, 0); err == nil {
		t.Fatal("zero increment bypassed missing-period validation")
	}
}

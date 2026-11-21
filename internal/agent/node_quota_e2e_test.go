package agent_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// A real two-node transport test. Website TCP connections are severed, while
// the independently persisted node quota continues renewing short core leases.
func TestNodeQuotaRealWebsiteOutageAndReconciliation(t *testing.T) {
	core, binary := os.Getenv("HQIP_E2E_CORE"), os.Getenv("HQIP_SINGBOX_EXE")
	if core == "" || binary == "" {
		t.Skip("isolated real core and sing-box paths required")
	}
	f := newPortalE2E(t, core)
	for _, n := range f.nodes {
		if err := f.db.SetOfflineAdmissionEnabled(n.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	f.wait(15*time.Second, func() bool {
		for _, n := range f.nodes {
			var wanted, applied int64
			_ = f.db.DB.QueryRow("SELECT desired_revision,applied_revision FROM nodes WHERE id=?", n.ID).Scan(&wanted, &applied)
			if wanted > applied {
				return false
			}
		}
		return true
	}, "node quota capability not applied")
	settings, err := f.db.TrafficBillingSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.SaveTrafficBillingSettings(model.TrafficBillingInput{Enabled: true, ExpectedRevision: settings.Revision, Hysteria2: 150, VLESS: 150}, "quota-e2e"); err != nil {
		t.Fatal(err)
	}
	user, subscription, passwords := f.account("node-quota", 8<<20, 2)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.CopyN(w, zeroE2EReader{}, 4096) }))
	defer target.Close()
	first := f.client("sing-box", binary, 0, passwords[0], target.Certificate())
	defer first.stop()
	second := f.client("sing-box", binary, 1, passwords[1], target.Certificate())
	defer second.stop()
	f.fetch(first, target.URL, 4096)
	f.fetch(second, target.URL, 4096)
	f.wait(15*time.Second, func() bool {
		var count int
		_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM traffic_lease_billing b JOIN portal_leases l ON l.id=b.lease_id WHERE l.subscription_id=? AND json_extract(b.state,'$.nodeQuota.revision')>0", subscription.ID).Scan(&count)
		return count == 2
	}, "real connections did not obtain node-owned quota")
	before, err := f.db.UserSubscription(user.ID, subscription.ID)
	if err != nil || before.ReservedBytes != 0 {
		t.Fatal("central reservation remained", before, err)
	}
	f.offline.Store(true)
	cut := time.Now()
	for time.Since(cut) < 125*time.Second {
		f.fetch(first, target.URL, 4096)
		f.fetch(second, target.URL, 4096)
		time.Sleep(5 * time.Second)
	}
	t.Log("Both real nodes forwarded past the 120-second short-lease boundary with website TCP unavailable")
	f.offline.Store(false)
	f.wait(30*time.Second, func() bool {
		e, err := f.db.UserSubscription(user.ID, subscription.ID)
		return err == nil && e.UsedBytes > before.UsedBytes+150_000 && e.ReservedBytes == 0
	}, "node usage did not reconcile after website recovery")
	first.stop()
	second.stop()
	e, err := f.db.UserSubscription(user.ID, subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.RevokeSubscription(user.ID, subscription.ID, e.Revision); err != nil {
		t.Fatal(err)
	}
	f.wait(55*time.Second, func() bool {
		var open int
		_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=? AND closed=0", subscription.ID).Scan(&open)
		return open == 0
	}, "revoked node parents did not submit exact finals")
	var raw int64
	_ = f.db.DB.QueryRow("SELECT SUM(upload+download) FROM portal_leases WHERE subscription_id=?", subscription.ID).Scan(&raw)
	e, err = f.db.UserSubscription(user.ID, subscription.ID)
	if err != nil || e.UsedBytes != raw*150/100 || e.ReservedBytes != 0 {
		t.Fatalf("node billing mismatch: raw=%d subscription=%+v error=%v", raw, e, err)
	}
}

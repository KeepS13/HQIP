package agent_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Exercise real QUIC disconnect detection, central slot release, another node,
// and cached-parent reacquisition. No administrative revoke repairs the state.
func TestConnectionSlotsRealTwoNodeSwitchAndReconnect(t *testing.T) {
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
	user, subscription, passwords := f.account("slot-switch", 8<<20, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.CopyN(w, zeroE2EReader{}, 4096)
	}))
	defer target.Close()
	for _, node := range []int{0, 1, 0} {
		client := f.client("sing-box", binary, node, passwords[node], target.Certificate())
		defer client.stop()
		f.fetch(client, target.URL, 4096)
		e, err := f.db.UserSubscription(user.ID, subscription.ID)
		if err != nil || e.Occupied != 1 || e.ReservedBytes != 0 {
			t.Fatal("real connection did not hold exactly one slot", e, err)
		}
		var rate, ceiling int64
		if err = f.db.DB.QueryRow("SELECT l.bandwidth,r.ceiling FROM portal_leases l JOIN portal_rates r ON l.id=r.lease_id WHERE l.subscription_id=? AND l.closed=0 AND l.expires>?", subscription.ID, time.Now().Unix()).Scan(&rate, &ceiling); err != nil || rate != 100000 || ceiling < rate {
			t.Fatal("reacquired slot did not reserve full rate", rate, ceiling, err)
		}
		client.stop() // Forced termination; the real core detects QUIC idle close.
		f.wait(45*time.Second, func() bool {
			e, _ := f.db.UserSubscription(user.ID, subscription.ID)
			return e != nil && e.Occupied == 0 && e.UsedBytes > 0
		}, "disconnected client retained an online slot")
	}
	var parents int
	_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=?", subscription.ID).Scan(&parents)
	if parents != 2 {
		t.Fatal("reconnection did not reuse its durable parent ledger", parents)
	}
}

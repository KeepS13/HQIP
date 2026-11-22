package agent_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Real controller + two Agents + HY2 transports reproduce the administrative
// deadline edit without production data. Each stopped client must release only
// its own slot, and the same credential must reconnect under the new deadline.
func TestOfflineExpiryRealClientsReleaseAfterAdministrativeEdit(t *testing.T) {
	core, binary := os.Getenv("HQIP_E2E_CORE"), os.Getenv("HQIP_SINGBOX_EXE")
	if core == "" || binary == "" {
		t.Skip("isolated real core and sing-box paths required")
	}
	f := newPortalE2E(t, core)
	for _, node := range f.nodes {
		if err := f.db.SetOfflineAdmissionEnabled(node.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	f.wait(15*time.Second, func() bool {
		var pending int
		return f.db.DB.QueryRow("SELECT COUNT(*) FROM nodes WHERE applied_revision<desired_revision").Scan(&pending) == nil && pending == 0
	}, "node quota configuration not acknowledged")
	user, sub, passwords := f.account("expiry-edit", 8<<20, 2)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.CopyN(w, zeroE2EReader{}, 4096)
	}))
	defer target.Close()
	first := f.client("sing-box", binary, 0, passwords[0], target.Certificate())
	defer first.stop()
	f.fetch(first, target.URL, 4096)
	second := f.client("sing-box", binary, 1, passwords[1], target.Certificate())
	defer second.stop()
	f.fetch(second, target.URL, 4096)
	current, err := f.db.UserSubscription(user.ID, sub.ID)
	if err != nil || current.Occupied != 2 {
		t.Fatal("fixture did not fill both slots", current, err)
	}
	var previousVersion int64
	if err = f.db.DB.QueryRow("SELECT MAX(report_version) FROM portal_offline_grants").Scan(&previousVersion); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if _, err = f.db.UpdateSubscriptionExpiry(user.ID, sub.ID, deadline, current.Revision); err != nil {
		t.Fatal(err)
	}
	f.wait(25*time.Second, func() bool {
		var version, until int64
		err := f.db.DB.QueryRow("SELECT MIN(o.report_version),MAX(l.expires) FROM portal_offline_grants o JOIN portal_leases l ON l.id=o.lease_id").Scan(&version, &until)
		return err == nil && version >= previousVersion+2 && until == deadline.Unix()
	}, "shorter deadline poisoned report acknowledgements")
	f.fetch(first, target.URL, 4096)
	f.fetch(second, target.URL, 4096)
	first.stop()
	f.wait(45*time.Second, func() bool {
		current, err := f.db.UserSubscription(user.ID, sub.ID)
		return err == nil && current.Occupied == 1
	}, "first disconnected instance kept its slot")
	f.fetch(second, target.URL, 4096)
	second.stop()
	f.wait(45*time.Second, func() bool {
		current, err := f.db.UserSubscription(user.ID, sub.ID)
		return err == nil && current.Occupied == 0 && current.UsedBytes > 0
	}, "ended instances still occupied subscription slots")
	reconnected := f.client("sing-box", binary, 0, passwords[0], target.Certificate())
	defer reconnected.stop()
	f.fetch(reconnected, target.URL, 4096)
	reconnected.stop()
	f.wait(45*time.Second, func() bool {
		current, err := f.db.UserSubscription(user.ID, sub.ID)
		return err == nil && current.Occupied == 0 && current.ExpiresAt.Equal(deadline)
	}, "reconnected instance did not release its slot")
}

package agent_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Uses the existing isolated two-node harness, real HY2, sing-box and Mihomo.
// The outage closes control TCP connections; no host proxy or routes change.
func TestOfflineRealHY2ContinuesAndReconnects(t *testing.T) {
	core, singbox, mihomo := os.Getenv("HQIP_E2E_CORE"), os.Getenv("HQIP_SINGBOX"), os.Getenv("HQIP_MIHOMO")
	if core == "" || singbox == "" || mihomo == "" {
		t.Skip("real HY2/sing-box/Mihomo paths required")
	}
	f := newPortalE2E(t, core)
	for _, n := range f.nodes {
		if err := f.db.SetOfflineAdmissionEnabled(n.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	f.wait(15*time.Second, func() bool {
		for _, n := range f.nodes {
			var want, applied int64
			_ = f.db.DB.QueryRow("SELECT desired_revision,applied_revision FROM nodes WHERE id=?", n.ID).Scan(&want, &applied)
			if applied < want {
				return false
			}
		}
		return true
	}, "Agents did not acknowledge offline capability rollout")
	u, subscription, passwords := f.account("offline-grace", 64<<20, 2)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.CopyN(w, zeroE2EReader{}, 4096) }))
	defer target.Close()
	first := f.client("sing-box", singbox, 0, passwords[0], target.Certificate())
	defer first.stop()
	f.fetch(first, target.URL, 4096)
	second := f.client("mihomo", mihomo, 1, passwords[1], target.Certificate())
	defer second.stop()
	f.fetch(second, target.URL, 4096)
	f.wait(15*time.Second, func() bool {
		var count int
		_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM portal_offline_grants").Scan(&count)
		return count == 2
	}, "real transports did not use offline parents")
	var deadline int64
	_ = f.db.DB.QueryRow("SELECT MIN(expires) FROM portal_leases WHERE subscription_id=?", subscription.ID).Scan(&deadline)
	cut := time.Now()
	f.offline.Store(true)
	for time.Since(cut) < 125*time.Second {
		f.fetch(first, target.URL, 4096)
		f.fetch(second, target.URL, 4096)
		time.Sleep(5 * time.Second)
	}
	t.Log("Both real clients forwarded through two nodes after 125 seconds of complete control outage")
	first.stop()
	// A killed process cannot claim immediate closure. The server observes QUIC
	// idle closure, then the same persisted parent permits one replacement.
	replacement := f.client("sing-box", singbox, 0, passwords[0], target.Certificate())
	defer replacement.stop()
	replacement.http.Timeout = 3 * time.Second
	f.wait(50*time.Second, func() bool {
		r, err := replacement.http.Get(target.URL)
		if err != nil {
			return false
		}
		defer r.Body.Close()
		n, err := io.Copy(io.Discard, r.Body)
		return err == nil && n == 4096
	}, "original-node reconnect required central service")
	var unchanged int64
	_ = f.db.DB.QueryRow("SELECT MIN(expires) FROM portal_leases WHERE subscription_id=?", subscription.ID).Scan(&unchanged)
	if unchanged != deadline {
		t.Fatal("offline reconnect extended original deadline")
	}
	wrong := f.client("sing-box", singbox, 1, passwords[0], target.Certificate())
	defer wrong.stop()
	wrong.http.Timeout = 2 * time.Second
	if r, err := wrong.http.Get(target.URL); err == nil {
		r.Body.Close()
		t.Fatal("node-bound credential used on another node")
	}
	f.offline.Store(false)
	f.wait(20*time.Second, func() bool {
		e, err := f.db.UserSubscription(u.ID, subscription.ID)
		return err == nil && e.UsedBytes > 150<<10
	}, "offline cumulative usage not reconciled")
	e, err := f.db.UserSubscription(u.ID, subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.RevokeSubscription(u.ID, subscription.ID, e.Revision); err != nil {
		t.Fatal(err)
	}
	f.wait(20*time.Second, func() bool {
		e, err := f.db.UserSubscription(u.ID, subscription.ID)
		return err == nil && e.ReservedBytes == 0 && e.EmergencyReservedBytes == 0
	}, "revoked grant finals did not release confirmed reservations")
	var total int64
	_ = f.db.DB.QueryRow("SELECT SUM(upload+download) FROM portal_leases WHERE subscription_id=?", subscription.ID).Scan(&total)
	e, _ = f.db.UserSubscription(u.ID, subscription.ID)
	if e.UsedBytes != total {
		t.Fatalf("offline usage reconciliation mismatch total=%d used=%d", total, e.UsedBytes)
	}
	t.Logf("Original-node reconnection, cross-node rejection, revocation and exact cumulative settlement passed: %d bytes", total)
}

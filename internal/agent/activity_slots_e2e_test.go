package agent_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

// Two isolated real client processes remain running throughout. Closing HTTP
// targets must return capacity even when their HY2 transports send keepalives.
// Active streams, rejected reacquisition and accounting use the real controller,
// Agents, node cores and durable ledgers, without manual lease cleanup.
func TestActivitySlotsRealProbesReleaseWhileClientsStayOpen(t *testing.T) {
	core, singbox := os.Getenv("HQIP_E2E_CORE"), os.Getenv("HQIP_SINGBOX_EXE")
	if core == "" || singbox == "" {
		t.Skip("isolated core and sing-box paths required")
	}
	cases := []struct{ kind, binary string }{{"sing-box", singbox}}
	if mihomo := os.Getenv("HQIP_MIHOMO"); mihomo != "" {
		cases = append(cases, struct{ kind, binary string }{"mihomo", mihomo})
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			f := newPortalE2E(t, core)
			for _, n := range f.nodes {
				if err := f.db.SetOfflineAdmissionEnabled(n.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			f.wait(15*time.Second, func() bool {
				for _, n := range f.nodes {
					var wanted, applied int64
					if err := f.db.DB.QueryRow("SELECT desired_revision,applied_revision FROM nodes WHERE id=?", n.ID).Scan(&wanted, &applied); err != nil || wanted > applied {
						return false
					}
				}
				return true
			}, "node quota capability not applied")
			user, sub, passwords := f.account("activity-"+tc.kind, 64<<20, 1)
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "4096")
				_, _ = io.CopyN(w, zeroE2EReader{}, 4096)
			}))
			defer target.Close()
			first := f.client("sing-box", singbox, 0, passwords[0], target.Certificate())
			defer first.stop()
			second := f.client(tc.kind, tc.binary, 1, passwords[1], target.Certificate())
			defer second.stop()
			idle := func() {
				t.Helper()
				f.wait(5*time.Second, func() bool {
					e, err := f.db.UserSubscription(user.ID, sub.ID)
					return err == nil && e.Occupied == 0 && e.UsedBytes > 0
				}, "completed probe retained online capacity")
			}
			probe := func(c *portalE2EClient) {
				t.Helper()
				r, _ := http.NewRequest(http.MethodGet, target.URL, nil)
				r.Close = true // A completed test closes its target, not its HY2 process.
				response, err := c.http.Do(r)
				if err != nil {
					t.Fatal("latency-style request failed", err)
				}
				n, err := io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if err != nil || n != 4096 {
					t.Fatal("final prematurely truncated buffered response", n, err)
				}
				idle()
			}
			for i := 0; i < 4; i++ {
				probe(first)
				probe(second)
			}
			// A persistent target TCP socket is genuine active use and holds the
			// only slot. Authenticating another QUIC must not bypass that limit.
			f.fetch(first, target.URL, 4096)
			current, err := f.db.UserSubscription(user.ID, sub.ID)
			if err != nil || current.Occupied != 1 {
				t.Fatal("live target did not hold capacity", current, err)
			}
			f.rejectSourceIP(1, passwords[1], "127.0.0.2")
			f.fetch(first, target.URL, 4096)
			first.http.CloseIdleConnections() // Explicitly closes the real target.
			idle()
			probe(second)
			probe(first) // Reuse original process after another node took the slot.
			if tc.kind == "mihomo" {
				// Exercise the actual built-in URL-test implementation, not only
				// requests through the proxy. The loopback target has no external
				// DNS or certificate dependency and belongs to this test alone.
				latencyTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
				defer latencyTarget.Close()
				for i := 0; i < 3; i++ {
					mihomoDelay(t, second.controller, latencyTarget.URL)
					idle()
					probe(first)
				}
			}
			// Keep both processes alive beyond a lease renewal interval. No
			// periodic QUIC keepalive may recreate or renew online occupation.
			time.Sleep(6 * time.Second)
			current, err = f.db.UserSubscription(user.ID, sub.ID)
			if err != nil || current.Occupied != 0 || current.ReservedBytes != 0 {
				t.Fatal("idle transports retained a slot or unsettled reservation", current, err)
			}
			var parents int
			var billed int64
			if err = f.db.DB.QueryRow("SELECT COUNT(*),COALESCE(SUM(upload+download),0) FROM portal_leases WHERE subscription_id=?", sub.ID).Scan(&parents, &billed); err != nil {
				t.Fatal(err)
			}
			if parents != 2 || billed != current.UsedBytes || billed < 12*4096 {
				t.Fatal("episodes lost bytes or failed to reuse bounded parent ledgers", parents, billed, current.UsedBytes)
			}
			t.Logf("two running clients: probes release within 5s, genuine use enforces one slot, settled bytes=%d", billed)
		})
	}
}

func mihomoDelay(t *testing.T, controller, target string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://"+controller+"/proxies/node/delay?timeout=5000&url="+url.QueryEscape(target), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer isolated-test-controller-secret")
	c := &http.Client{Timeout: 7 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer c.CloseIdleConnections()
	response, err := c.Do(r)
	if err != nil {
		t.Fatal("built-in Mihomo delay failed", err)
	}
	defer response.Body.Close()
	var result struct {
		Delay *uint `json:"delay"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK || result.Delay == nil {
		t.Fatal("Mihomo did not return a real delay", response.StatusCode, err)
	}
}

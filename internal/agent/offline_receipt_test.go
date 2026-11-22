package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Administrative expiry changes are authoritative. The receipt must settle
// before a child can renew or release; rejecting a shorter deadline strands
// the old active report forever, even after every child has disconnected.
func TestOfflineReceiptShortenedExpiryRenewsAndReleases(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "expiry-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion, s.Online = model.ConnectionSlotsVersion, true
	a, reports := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	if _, err := a.localOfflineGrant(&s, "child", now); err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Hour)
	reports.mu.Lock()
	reports.grant.OfflineUntil = deadline
	reports.mu.Unlock()
	if err := a.syncOfflineState(context.Background(), &s, true); err != nil {
		t.Fatal("legitimate expiry edit rejected", err)
	}
	if s.Pending != nil || !s.Grant.OfflineUntil.Equal(deadline) || s.Denied {
		t.Fatal("expiry update did not apply", s)
	}
	if _, err := a.localOfflineGrant(&s, "child", now); err != nil {
		t.Fatal("valid active connection interrupted", err)
	}
	if err := s.updateChild("child", model.LeaseUsage{Closed: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := a.syncOfflineState(context.Background(), &s, true); err != nil {
		t.Fatal(err)
	}
	if err := a.releaseIdleOfflineSlot(context.Background(), &s); err != nil || !s.Grant.SlotReleased {
		t.Fatal("disconnected slot not released", err)
	}
	if err := a.prepareOfflineSlot(context.Background(), &s); err != nil || s.Grant.SlotReleased {
		t.Fatal("released slot could not reconnect", err)
	}
}

// Reproduce the persisted production failure, including a stale active report,
// Denied set by the old validator, and final counters arriving after that report.
func TestOfflineReceiptRepairsPersistedShortenedExpiryAfterRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now, "stuck-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion, s.Online = model.ConnectionSlotsVersion, true
	a, reports := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	if _, err := a.localOfflineGrant(&s, "child", now); err != nil {
		t.Fatal(err)
	}
	pending, err := s.usage(now)
	if err != nil || !pending.Active {
		t.Fatal("fixture did not hold an active report", pending, err)
	}
	s.Pending, s.NextVersion, s.Denied, s.Online = &pending, pending.Version, true, false
	if err = s.updateChild("child", model.LeaseUsage{Closed: true}, now); err != nil {
		t.Fatal(err)
	}
	if err = a.ledger.saveOffline(s); err != nil {
		t.Fatal(err)
	}
	path := a.cfg.StatePath
	_ = a.ledger.close()
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	reports.mu.Lock()
	reports.grant.OfflineUntil = now.Add(time.Hour)
	reports.mu.Unlock()
	if err = a.syncOfflineGrants(context.Background()); err != nil {
		t.Fatal(err)
	}
	states, err := a.ledger.offlineStates()
	if err != nil || len(states) != 1 {
		t.Fatal(states, err)
	}
	s = states[0]
	if s.Pending != nil || !s.Grant.SlotReleased || !s.Grant.Closed || !s.Children["child"].Closed {
		t.Fatal("old poisoned report still occupies a slot", s)
	}
	reports.mu.Lock()
	defer reports.mu.Unlock()
	var replay model.OfflineUsage
	if len(reports.bodies) != 2 || json.Unmarshal([]byte(reports.bodies[0]), &replay) != nil || !reflect.DeepEqual(replay, pending) {
		t.Fatal("repair rewrote the in-flight accounting report", reports.bodies)
	}
}

// A committed response can arrive after its authorization expires. It remains
// an accounting receipt, but cannot authorize another child or erase unknown
// counters. A subsequent explicit release must still reach the controller.
func TestOfflineReceiptExpiredReplayStillReleasesUnknownChild(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := quotaTestState(now.Add(-2*time.Hour), "expired-parent", "hysteria2", 100_000_000)
	s.Grant.ConnectionSlotsVersion = model.ConnectionSlotsVersion
	s.Grant.OfflineUntil = now.Add(-time.Hour)
	s.Children["unknown"] = offlineChild{ID: "unknown", Ended: true, Normal: 100_000}
	s.Pending = &model.OfflineUsage{Version: 1, Active: true}
	s.NextVersion = 1
	a, _ := idleReportFixture(t, s)
	a.now = func() time.Time { return now }
	if err := a.syncOfflineState(context.Background(), &s, true); err != nil || s.Pending != nil {
		t.Fatal("expired committed receipt blocks settlement", err)
	}
	if _, err := a.localOfflineGrant(&s, "new-child", now); err == nil {
		t.Fatal("expired receipt authorized forwarding")
	}
	if err := a.releaseIdleOfflineSlot(context.Background(), &s); err != nil || !s.Grant.SlotReleased {
		t.Fatal("expired receipt could not release a proven-ended connection", err)
	}
	if s.Grant.Closed || s.Children["unknown"].Normal != 100_000 || s.Children["unknown"].Closed {
		t.Fatal("releasing a slot discarded unknown final usage", s)
	}
}

func TestOfflineReceiptInvalidResponseRetainsExactPendingReport(t *testing.T) {
	for _, change := range []string{"identity", "counters", "version", "future-authorization", "invalid-deadline", "protocol", "quota-revision"} {
		t.Run(change, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			s := quotaTestState(now, "invalid-parent", "hysteria2", 100_000_000)
			s.Grant.ConnectionSlotsVersion = model.ConnectionSlotsVersion
			g := s.Grant
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var u model.OfflineUsage
				_ = json.NewDecoder(r.Body).Decode(&u)
				g.ReportVersion = u.Version
				switch change {
				case "identity":
					g.SubscriptionID = "other-subscription"
				case "counters":
					g.UploadBytes++
				case "version":
					g.ReportVersion++
				case "future-authorization":
					g.AuthorizedAt = now.Add(time.Hour)
				case "invalid-deadline":
					g.OfflineUntil = g.AuthorizedAt
				case "protocol":
					copy := *g.Billing
					copy.Protocol = "vless"
					g.Billing = &copy
				case "quota-revision":
					copy := *g.NodeQuota
					copy.Revision = 0
					g.NodeQuota = &copy
				}
				_ = json.NewEncoder(w).Encode(g)
			}))
			defer server.Close()
			a := testAgent(t, "", server.URL)
			a.now = func() time.Time { return now }
			pending, _ := s.usage(now)
			if err := a.syncOfflineState(context.Background(), &s, true); err == nil || s.Pending == nil || !reflect.DeepEqual(*s.Pending, pending) {
				t.Fatal("invalid receipt accepted or pending report altered", err, s)
			}
		})
	}
}

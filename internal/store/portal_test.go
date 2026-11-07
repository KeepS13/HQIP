package store

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func portalFixture(t *testing.T, slots int, quota *int64) (*Store, *time.Time, model.PortalUser, model.SubscriptionTemplate, model.Node, string) {
	t.Helper()
	s, now := fixture(t)
	n := newNode(t, s, "portal-node")
	if e := s.SetStatus(n.ID, "ready", "test", false); e != nil {
		t.Fatal(e)
	}
	_, _ = s.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]' WHERE id=?", n.ID)
	u, e := s.RegisterPortal("Person@Example.com", "test-user-password-123")
	if e != nil {
		t.Fatal(e)
	}
	bandwidth := float64(8)
	p, e := s.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "订阅", Enabled: true, Listed: true, ValidityDays: 1, NodeIDs: []string{n.ID}, DefaultNodeID: n.ID, BandwidthMbps: &bandwidth, QuotaBytes: quota, MaxConnections: slots}, 0)
	if e != nil {
		t.Fatal(e)
	}
	sub, e := s.AssignSubscription(u.ID, p.ID, "fixture-grant", 1, p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PortalSubscription(u.ID, sub.ID, "create"); e != nil {
		t.Fatal(e)
	}
	password, e := s.PortalCredential(u.ID, sub.ID, n.ID, "subscription")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ActivatePortal(); e != nil {
		t.Fatal(e)
	}
	u, _ = s.PortalUser(u.ID)
	return s, now, u, p, n, password
}
func acquire(t *testing.T, s *Store, n model.Node, password, conn string) model.ConnectionLease {
	t.Helper()
	l, e := s.AcquireLease(n.ID, strings.Repeat(conn, 32), password)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func renew(t *testing.T, s *Store, l model.ConnectionLease, up, down int64, closed bool) model.ConnectionLease {
	t.Helper()
	v, e := s.RenewLease(l.NodeID, l.ID, model.LeaseUsage{UploadBytes: up, DownloadBytes: down, Closed: closed, AppliedRateVersion: l.RateVersion})
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestPortalUpgradeDoesNotActivateLegacyRevocation(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "legacy")
	d := newDevice(t, s, "legacy-device")
	before, e := s.Desired(n.ID)
	if e != nil {
		t.Fatal(e)
	}
	if s.PortalEnabled() || before.PortalEnabled {
		t.Fatal("schema migration activated portal")
	}
	found := false
	for _, v := range before.Devices {
		if v.ID == d.ID {
			found = v.Enabled
		}
	}
	if !found {
		t.Fatal("migration revoked legacy")
	}
	if e = s.ActivatePortal(); e != nil {
		t.Fatal(e)
	}
	after, e := s.Desired(n.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range after.Devices {
		if v.ID != ProbeID(n.ID) && v.Enabled {
			t.Fatal("legacy authorization survived activation")
		}
	}
	if !after.PortalEnabled {
		t.Fatal("activation not sent to Agent")
	}
}
func TestPortalLeaseConcurrentSlotsAndBudgetConservation(t *testing.T) {
	q := int64(20 << 20)
	s, _, u, _, n, password := portalFixture(t, 2, &q)
	var wg sync.WaitGroup
	var mu sync.Mutex
	leases := []model.ConnectionLease{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, e := s.AcquireLease(n.ID, strings.Repeat(string(rune('a'+i)), 32), password)
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				leases = append(leases, l)
			} else if !errors.Is(e, ErrSlots) {
				t.Errorf("unexpected admission error %v", e)
			}
		}(i)
	}
	wg.Wait()
	if len(leases) != 2 {
		t.Fatalf("slots oversubscribed: %d", len(leases))
	}
	e, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if e.ReservedBytes > q || e.Occupied != 2 {
		t.Fatalf("budget/slot invariant %+v", e)
	}
	for _, l := range leases {
		same, err := s.AcquireLease(n.ID, func() string {
			var x string
			_ = s.DB.QueryRow("SELECT connection_id FROM portal_leases WHERE id=?", l.ID).Scan(&x)
			return x
		}(), password)
		if err != nil || same.ID != l.ID {
			t.Fatal("admission not idempotent")
		}
	}
}
func TestPortalDynamicRateWaitsForDownwardACK(t *testing.T) {
	s, _, _, _, n, password := portalFixture(t, 3, nil)
	a := acquire(t, s, n, password, "a")
	if a.RatePending || a.BandwidthBytesPerSecond != 1_000_000 {
		t.Fatalf("single connection must receive full bandwidth: %+v", a)
	}
	a = renew(t, s, a, 0, 0, false)
	b := acquire(t, s, n, password, "b")
	if !b.RatePending {
		t.Fatal("new connection started before old rate ACK")
	}
	a = renew(t, s, a, 0, 0, false)
	if a.BandwidthBytesPerSecond != 500_000 {
		t.Fatalf("old connection not given reduced share: %+v", a)
	}
	b = renew(t, s, b, 0, 0, false)
	if !b.RatePending {
		t.Fatal("old connection has not acknowledged reduced share")
	}
	a = renew(t, s, a, 0, 0, false)
	b = renew(t, s, b, 0, 0, false)
	if b.RatePending || b.BandwidthBytesPerSecond != 500_000 {
		t.Fatalf("new connection never received freed share %+v", b)
	}
	_ = renew(t, s, b, 0, 0, true)
	a = renew(t, s, a, 0, 0, false)
	if a.BandwidthBytesPerSecond != 1_000_000 {
		t.Fatal("remaining connection did not regain full bandwidth")
	}
	if _, e := s.RenewLease(n.ID, a.ID, model.LeaseUsage{AppliedRateVersion: a.RateVersion + 10}); !errors.Is(e, ErrConflict) {
		t.Fatal("future ACK accepted")
	}
}
func TestPortalBudgetReplayExpiryAndLateSettlement(t *testing.T) {
	q := model.MinimumSubscriptionLeaseBytes + 1024
	s, now, u, _, n, password := portalFixture(t, 1, &q)
	l := acquire(t, s, n, password, "a")
	l = renew(t, s, l, 100, 200, false)
	l = renew(t, s, l, 100, 200, false)
	e, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if e.UsedBytes != 300 || e.ReservedBytes != q-300 {
		t.Fatalf("duplicate usage charged %+v", e)
	}
	*now = now.Add(121 * time.Second)
	if _, err := s.RenewLease(n.ID, l.ID, model.LeaseUsage{UploadBytes: 100, DownloadBytes: 200}); !errors.Is(err, ErrEntitlement) {
		t.Fatal("expired lease revived")
	}
	if _, err := s.AcquireLease(n.ID, strings.Repeat("b", 32), password); !errors.Is(err, ErrEntitlement) {
		t.Fatal("unsettled budget was reissued")
	}
	l = renew(t, s, l, 150, 250, true)
	_ = renew(t, s, l, 150, 250, true)
	e, _ = s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if e.UsedBytes != 400 || e.ReservedBytes != 0 {
		t.Fatalf("late settlement not exact %+v", e)
	}
	next := acquire(t, s, n, password, "b")
	if next.BudgetBytes != q-400 {
		t.Fatal("wrong remaining quota")
	}
	if _, err := s.RenewLease(n.ID, next.ID, model.LeaseUsage{UploadBytes: q - 399, Closed: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("overspend accepted")
	}
	if _, err := s.RenewLease("another-node", next.ID, model.LeaseUsage{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross node settlement accepted")
	}
}
func TestPortalTrafficFeedsNodeLedgerWithoutQuotaDoubleCharge(t *testing.T) {
	s, now, u, _, n, password := portalFixture(t, 1, nil)
	l := acquire(t, s, n, password, "a")
	_ = renew(t, s, l, 80, 120, false)
	desired, _ := s.Desired(n.ID)
	r := model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Online: map[string]int{l.AccountingID: 1}, Daily: []model.UsageBucket{{DeviceID: l.AccountingID, Start: DayStart(*now), Upload: 80, Download: 120}}, Minutes: []model.UsageBucket{{DeviceID: l.AccountingID, Start: now.Unix() / 60 * 60, Upload: 80, Download: 120}}}
	if e := s.Report(n.ID, r); e != nil {
		t.Fatal(e)
	}
	if e := s.Report(n.ID, r); e != nil {
		t.Fatal(e)
	}
	node, e := s.Node(n.ID)
	if e != nil || node.TotalUpload != 80 || node.TotalDownload != 120 {
		t.Fatalf("portal usage absent from node ledger %+v %v", node, e)
	}
	totals, e := s.UsageTotals()
	if e != nil || totals.TotalUpload != 80 || totals.TotalDownload != 120 {
		t.Fatal("server aggregate omitted portal traffic")
	}
	ent, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if ent.UsedBytes != 200 {
		t.Fatal("Agent report double charged quota")
	}
}
func TestPortalPKCERefreshReplayAndScopeIsolation(t *testing.T) {
	s, _ := fixture(t)
	u, e := s.RegisterPortal("hello@example.com", "password-good-enough")
	if e != nil {
		t.Fatal(e)
	}
	verifier := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "http://127.0.0.1:41234/callback"
	for _, bad := range []string{"https://evil.invalid/callback", "http://localhost:12/callback", "http://127.0.0.1:12/callback?evil=1"} {
		if _, _, e = s.BeginPortalAuthorization(challenge, bad, strings.Repeat("s", 32), "desktop", "installation-1"); e == nil {
			t.Fatal("unsafe callback accepted")
		}
	}
	id, _, e := s.BeginPortalAuthorization(challenge, redirect, strings.Repeat("s", 32), "desktop", "installation-1")
	if e != nil {
		t.Fatal(e)
	}
	browser, _, e := s.PortalLogin(u.Email, "password-good-enough")
	if e != nil {
		t.Fatal(e)
	}
	target, e := s.ApprovePortalAuthorization(id, u.ID, browser.SessionID)
	if e != nil {
		t.Fatal(e)
	}
	parsed, _ := url.Parse(target)
	code := parsed.Query().Get("code")
	if _, e = s.ExchangePortalCode(code, strings.Repeat("b", 43), redirect, "installation-1"); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("wrong verifier accepted")
	}
	tokens, e := s.ExchangePortalCode(code, verifier, redirect, "installation-1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExchangePortalCode(code, verifier, redirect, "installation-1"); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("authorization code replay accepted")
	}
	if _, _, _, e = s.PortalIdentity(tokens.AccessToken, "browser"); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("native token accepted as browser session")
	}
	rotated, e := s.PortalRefresh(tokens.RefreshToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PortalRefresh(tokens.RefreshToken); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("refresh replay accepted")
	}
	if _, _, _, e = s.PortalIdentity(rotated.AccessToken, "native"); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("refresh replay did not revoke family")
	}
}
func TestPortalSubscriptionRevocationLeavesNativeCredentialsIndependent(t *testing.T) {
	s, _, u, _, n, password := portalFixture(t, 2, nil)
	l := acquire(t, s, n, password, "a")
	session := nativeSubscriptionSession(t, s, u.ID, u.Subscriptions[0].ID)
	native, e := s.PortalCredential(u.ID, u.Subscriptions[0].ID, n.ID, session)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PortalSubscription(u.ID, u.Subscriptions[0].ID, "rotate"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AcquireLease(n.ID, strings.Repeat("b", 32), password); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("rotated subscription credential still accepted")
	}
	if _, e = s.RenewLease(n.ID, l.ID, model.LeaseUsage{}); !errors.Is(e, ErrEntitlement) {
		t.Fatal("old subscription connection not revoked")
	}
	same, e := s.PortalCredential(u.ID, u.Subscriptions[0].ID, n.ID, session)
	if e != nil || same != native {
		t.Fatal("rotation changed unrelated native credential")
	}
}

func TestPortalRestoreRequiresFinalLedgerReviewAndNeverReleasesReservations(t *testing.T) {
	s, now, u, _, n, password := portalFixture(t, 1, nil)
	l := acquire(t, s, n, password, "a")
	l = renew(t, s, l, 10, 20, false)
	tokens, _, err := s.PortalLogin(u.Email, "test-user-password-123")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if err = s.ReconcileRestore(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.PortalIdentity(tokens.AccessToken, "browser"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("restore revived browser session")
	}
	if link, _ := s.PortalSubscription(u.ID, u.Subscriptions[0].ID, "get"); link != "" {
		t.Fatal("restore revived subscription")
	}
	state, err := s.PortalRecovery()
	if err != nil || !state.Hold || state.Ready {
		t.Fatalf("restore recovery not held %+v %v", state, err)
	}
	if err = s.ResumePortalRecovery(state.Epoch, true, []RecoveryTotal{{UserID: u.ID, SubscriptionID: u.Subscriptions[0].ID, UsedBytes: 30}}); err == nil {
		t.Fatal("recovery bypassed old lease wait")
	}
	*now = now.Add(121 * time.Second)
	state, _ = s.PortalRecovery()
	if state.Ready {
		t.Fatal("offline node permitted recovery")
	}
	desired, _ := s.Desired(n.ID)
	if err = s.Report(n.ID, model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Daily: []model.UsageBucket{{DeviceID: l.AccountingID, Start: DayStart(*now), Upload: 40, Download: 60}}}); err != nil {
		t.Fatal(err)
	}
	state, err = s.PortalRecovery()
	if err != nil || !state.Ready || len(state.Users) != 1 || state.Users[0].MinimumUsedBytes != 100 {
		t.Fatalf("final ledger gap not represented %+v %v", state, err)
	}
	if err = s.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	if s.PortalSetting("portal_recovery_hold") != "true" {
		t.Fatal("activation bypassed recovery review")
	}
	if err = s.ResumePortalRecovery("wrong-epoch", true, []RecoveryTotal{{UserID: u.ID, SubscriptionID: u.Subscriptions[0].ID, UsedBytes: 100}}); err == nil {
		t.Fatal("wrong recovery epoch accepted")
	}
	if err = s.ResumePortalRecovery(state.Epoch, true, []RecoveryTotal{{UserID: u.ID, SubscriptionID: u.Subscriptions[0].ID, UsedBytes: 99}}); err == nil {
		t.Fatal("usage rollback accepted")
	}
	// Activating again increments desired revision; a fresh node ACK is required.
	desired, _ = s.Desired(n.ID)
	if err = s.Report(n.ID, model.AgentReport{Revision: desired.Revision, CoreHealthy: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.ResumePortalRecovery(state.Epoch, true, []RecoveryTotal{{UserID: u.ID, SubscriptionID: u.Subscriptions[0].ID, UsedBytes: 100}}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if after.UsedBytes != 100 || after.ReservedBytes != before.ReservedBytes {
		t.Fatalf("recovery lost known usage or returned uncertain budget %+v", after)
	}
	if s.PortalSetting("portal_recovery_hold") != "false" {
		t.Fatal("review did not resume")
	}
}

func TestPortalTurnstileAtomicSettingsCASAndTokenConsumption(t *testing.T) {
	s, _ := fixture(t)
	secret := "private-turnstile-secret"
	v, err := s.SaveTurnstile("site-one", []string{"panel.example.com"}, &secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	other := "another-private-turnstile-secret"
	if _, err = s.SaveTurnstile("site-two", []string{"other.example.com"}, &other, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale Turnstile settings accepted")
	}
	got, current, err := s.TurnstileVerificationConfig()
	if err != nil || got.SiteKey != "site-one" || current != secret {
		t.Fatal("failed CAS partially changed Turnstile pair")
	}
	if _, err = s.SaveTurnstile("site-two", []string{"*.example.com"}, &other, v.Revision); err == nil {
		t.Fatal("wildcard domain accepted")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.ConsumeTurnstile("same-captcha-token") {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("Turnstile token replay allowed %d times", accepted)
	}
}

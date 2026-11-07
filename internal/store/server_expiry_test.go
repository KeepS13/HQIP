package store

import (
	"errors"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestServerExpiryBackwardScheduleBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	for _, test := range []struct{ days, first int }{{15, 0}, {30, 0}, {45, 15}, {60, 30}, {90, 30}, {95, 5}} {
		t.Run(time.Duration(test.days*24).String(), func(t *testing.T) {
			expires := now.Add(time.Duration(test.days) * 24 * time.Hour)
			b := billingState{started: now.Add(-7 * 24 * time.Hour).Unix(), anchor: nextServerBillingAnchor(expires, now), expires: expires.Unix(), offset: 3}
			start, index, next := currentBillingCycle(b, now)
			if start != b.started || index != 3 {
				t.Fatal("saving moved current cycle", start, index)
			}
			if test.first == 0 {
				if next != 0 {
					t.Fatal("expiry or save time became a reset", next)
				}
			} else {
				want := now.Add(time.Duration(test.first) * 24 * time.Hour)
				if next != want.Unix() {
					t.Fatal("wrong backwards boundary", next, want)
				}
				start, index, _ = currentBillingCycle(b, want)
				if start != want.Unix() || index != 4 {
					t.Fatal("boundary did not advance once", start, index)
				}
			}
			_, final, next := currentBillingCycle(b, expires.Add(-time.Second))
			_, expired, after := currentBillingCycle(b, expires.Add(100*24*time.Hour))
			if final != expired || next != 0 || after != 0 {
				t.Fatal("final expiry reset the cycle")
			}
		})
	}
}

func TestServerExpiryCorrectionKeepsUsageIdentityAndReanchorsFuture(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	v, node, _ := serverPair(t, s)
	original := now.Add(90 * 24 * time.Hour)
	v = configureBilling(t, s, v, original)
	if v.Billing.NextResetAt == nil || !v.Billing.NextResetAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatal("initial setup did not plan backwards", v.Billing)
	}
	*now = now.Add(45 * 24 * time.Hour)
	rawBillingReport(t, s, node.ID, *now, 500, 800)
	before, err := s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := ServerExpiryRequest{ExpectedRevision: before.Revision, RequestID: "correction-request-1", ExpiresAt: now.Add(47 * 24 * time.Hour)}
	v, change, err := s.CorrectServerExpiry(v.ID, request, "expiry-admin")
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved := func(got, want model.Server) {
		t.Helper()
		if got.Billing.CycleID != want.Billing.CycleID || !got.Billing.CycleStartedAt.Equal(*want.Billing.CycleStartedAt) || got.Billing.CycleUpload != want.Billing.CycleUpload || got.Billing.CycleDownload != want.Billing.CycleDownload || got.MonthUpload != want.MonthUpload || got.TotalUpload != want.TotalUpload {
			t.Fatal("date edit changed accounting", got.Billing, want.Billing)
		}
	}
	assertPreserved(v, before)
	if v.Billing.NextResetAt == nil || !v.Billing.NextResetAt.Equal(now.Add(17*24*time.Hour)) || change.Kind != "correction" || change.Actor != "expiry-admin" || !change.PreviousExpiresAt.Equal(original) {
		t.Fatal("wrong correction", change, v.Billing)
	}
	n, err := s.Node(node.ID)
	if err != nil || n.Billing.CycleID != before.Billing.CycleID || n.Billing.CycleUpload != 500 {
		t.Fatal("node accounting changed", n.Billing, err)
	}
	replayed, replay, err := s.CorrectServerExpiry(v.ID, request, "expiry-admin")
	if err != nil || replay.ID != change.ID || replayed.Revision != v.Revision {
		t.Fatal("retry duplicated correction", err)
	}
	conflict := request
	conflict.ExpiresAt = request.ExpiresAt.Add(time.Hour)
	if _, _, err = s.CorrectServerExpiry(v.ID, conflict, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	conflict = request
	conflict.RequestID = "stale-correction"
	if _, _, err = s.CorrectServerExpiry(v.ID, conflict, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	for i, expiry := range []time.Time{time.Time{}, *now, now.Add(time.Minute), now.Add(time.Hour).Add(time.Nanosecond), time.Date(2201, 1, 1, 0, 0, 0, 0, time.UTC)} {
		bad := ServerExpiryRequest{ExpectedRevision: v.Revision, RequestID: "invalid-expiry-" + string(rune('a'+i)), ExpiresAt: expiry}
		if _, _, err = s.CorrectServerExpiry(v.ID, bad, "operator"); err == nil {
			t.Fatal("invalid expiry accepted", expiry)
		}
	}
	// Shortening across the old scheduled boundary does not restore older usage.
	request = ServerExpiryRequest{ExpectedRevision: v.Revision, RequestID: "shorten-correction", ExpiresAt: now.Add(7 * 24 * time.Hour)}
	short, _, err := s.CorrectServerExpiry(v.ID, request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved(short, v)
	if short.Billing.NextResetAt != nil {
		t.Fatal("short expiry retained old future boundary")
	}
	// Even a long-expired server keeps its final cycle until a new future boundary.
	*now = request.ExpiresAt.Add(65 * 24 * time.Hour)
	expired, err := s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	request = ServerExpiryRequest{ExpectedRevision: short.Revision, RequestID: "expired-correction", ExpiresAt: now.Add(65 * 24 * time.Hour)}
	v, _, err = s.CorrectServerExpiry(v.ID, request, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved(v, expired)
	if v.Billing.NextResetAt == nil || !v.Billing.NextResetAt.Equal(now.Add(5*24*time.Hour)) {
		t.Fatal("expired edit did not reanchor", v.Billing)
	}
	reopened, err := Open(s.DataDir, s.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Now = s.Now
	loaded, err := reopened.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved(loaded, v)
	boundary := *v.Billing.NextResetAt
	*now = boundary
	advanced, err := reopened.Server(v.ID)
	if err != nil || advanced.Billing.CycleID == v.Billing.CycleID || !advanced.Billing.CycleStartedAt.Equal(boundary) {
		t.Fatal("new boundary failed after restart", advanced.Billing, err)
	}
	// A real advance renewal also preserves this current cycle while reanchoring.
	months := 2
	renewed, _, err := s.RenewServer(v.ID, ServerRenewalRequest{ExpectedRevision: advanced.Revision, RequestID: "after-correction-renew", Months: &months}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved(renewed, advanced)
	if renewed.Billing.NextResetAt == nil || !renewed.Billing.NextResetAt.Equal(boundary.Add(30*24*time.Hour)) {
		t.Fatal("real renewal lost expiry grid", renewed.Billing)
	}
	*now = request.ExpiresAt.Add(100 * 24 * time.Hour)
	if _, replay, err = s.CorrectServerExpiry(v.ID, request, "operator"); err != nil || replay.RequestID != request.RequestID {
		t.Fatal("past successful request no longer replayable", err)
	}
	old, err := s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(40 * 24 * time.Hour)
	overdue, _, err := s.RenewServer(v.ID, ServerRenewalRequest{ExpectedRevision: old.Revision, RequestID: "true-overdue-after-correction", ExpiresAt: &expiry}, "operator")
	if err != nil || overdue.Billing.CycleID == old.Billing.CycleID || !overdue.Billing.CycleStartedAt.Equal(*now) || overdue.Billing.NextResetAt == nil || !overdue.Billing.NextResetAt.Equal(now.Add(10*24*time.Hour)) {
		t.Fatal("explicit overdue renewal failed after correction", overdue.Billing, err)
	}
}

func TestServerBillingOffsetMigrationPreservesLegacyCycle(t *testing.T) {
	s, now := fixture(t)
	v, _, _ := serverPair(t, s)
	*now = now.Truncate(time.Hour)
	v = configureBilling(t, s, v, now.Add(95*24*time.Hour))
	if _, err := s.DB.Exec("ALTER TABLE server_billing DROP COLUMN cycle_offset"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureServerBillingSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	if err := EnsureServerBillingSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Server(v.ID)
	if err != nil || loaded.Billing.CycleID != v.Billing.CycleID || !loaded.Billing.CycleStartedAt.Equal(*v.Billing.CycleStartedAt) || !loaded.Billing.NextResetAt.Equal(*v.Billing.NextResetAt) {
		t.Fatal("legacy schema migration changed cycle", loaded.Billing, err)
	}
}

func TestServerExpiryCorrectionRetainsCycleAlertAndUpdatesExpiryAlert(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	v, node, _ := serverPair(t, s)
	quota := int64(100)
	v, err := s.UpdateServerSettings(v.ID, v.Name, nil, &quota, v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	v = configureBilling(t, s, v, now.Add(48*time.Hour))
	rawBillingReport(t, s, node.ID, *now, 40, 40)
	cycleAlerts := collectBillingAlerts(t, s, "server.cycle")
	expiryAlerts := collectBillingAlerts(t, s, "server.expiry")
	if len(cycleAlerts) != 1 || len(expiryAlerts) != 1 {
		t.Fatal("initial alerts missing")
	}
	if _, err = s.UpdateAlert(cycleAlerts[0].ID, "operator", model.AlertUpdate{ExpectedRevision: cycleAlerts[0].Revision, Acknowledge: true}); err != nil {
		t.Fatal(err)
	}
	newExpiry := now.Add(45 * 24 * time.Hour)
	if _, _, err = s.CorrectServerExpiry(v.ID, ServerExpiryRequest{ExpectedRevision: v.Revision, RequestID: "correction-alerts", ExpiresAt: newExpiry}, "operator"); err != nil {
		t.Fatal(err)
	}
	got := collectBillingAlerts(t, s, "server.cycle")
	if len(got) != 1 || got[0].ID != cycleAlerts[0].ID || !got[0].Acknowledged || got[0].Status != "active" {
		t.Fatal("date correction reset quota acknowledgement", got)
	}
	got = collectBillingAlerts(t, s, "server.expiry")
	if len(got) != 1 || got[0].ID != expiryAlerts[0].ID || got[0].Status != "ended" {
		t.Fatal("old expiry warning did not end", got)
	}
	*now = newExpiry.Add(-48 * time.Hour)
	got = collectBillingAlerts(t, s, "server.expiry")
	active := 0
	for _, alert := range got {
		if alert.Status == "active" {
			active++
			if alert.ID == expiryAlerts[0].ID || alert.Acknowledged {
				t.Fatal("new date reused old warning", alert)
			}
		}
	}
	if active != 1 {
		t.Fatal("corrected expiry warning missing", got)
	}
}

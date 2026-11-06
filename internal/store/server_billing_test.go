package store

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func configureBilling(t *testing.T, s *Store, v model.Server, expires time.Time) model.Server {
	t.Helper()
	v, err := s.UpdateServerSettings(v.ID, v.Name, v.BandwidthMbps, v.MonthlyQuotaBytes, v.Revision, ServerBillingSetup{ExpiresAt: &expires, Actor: "billing-admin"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func rawBillingReport(t *testing.T, s *Store, node string, day time.Time, up, down int64, hours ...model.UsageBucket) {
	t.Helper()
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO daily(node_id,device_id,start,upload,download) VALUES(?,'accounting-test',?,?,?) ON CONFLICT(node_id,device_id,start) DO UPDATE SET upload=excluded.upload,download=excluded.download", node, DayStart(day), up, down); err != nil {
		t.Fatal(err)
	}
	for _, h := range hours {
		if _, err = tx.Exec("INSERT INTO native_hourly(node_id,device_id,start,upload,download,uncertain) VALUES(?,'accounting-test',?,?,?,?) ON CONFLICT(node_id,device_id,start) DO UPDATE SET upload=MAX(upload,excluded.upload),download=MAX(download,excluded.download),uncertain=MAX(uncertain,excluded.uncertain)", node, h.Start, h.Upload, h.Download, h.Uncertain); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec("UPDATE nodes SET managed=1,last_seen=?,core_healthy=1 WHERE id=?", s.Now().Unix(), node); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO node_hourly_coverage(node_id,since,through) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET since=MIN(since,excluded.since),through=MAX(through,excluded.through)", node, DayStart(day), s.Now().Add(-5*time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if err = RecordServerBillingUsage(tx, node, s.Now()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestServerBillingInitialConfigurationPreservesUsageAndSeparateLimits(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	v, a, b := serverPair(t, s)
	d := newDevice(t, s, "traffic")
	report(t, s, a, d, now.Add(-24*time.Hour), 100, 200)
	report(t, s, b, d, *now, 300, 400)
	if before, err := s.Server(v.ID); err != nil || before.Billing.Status != "unconfigured" || before.Billing.ExpiresAt != nil {
		t.Fatal("existing server guessed expiry", before, err)
	}
	expires := now.Add(12 * 24 * time.Hour)
	v = configureBilling(t, s, v, expires)
	if v.Billing.CycleUpload != 400 || v.Billing.CycleDownload != 600 || v.MonthUpload != 400 || v.Billing.CycleStartedAt.Unix() != MonthStart(*now) {
		t.Fatal("initial setup reset existing month", v.Billing)
	}
	n, err := s.Node(a.ID)
	if err != nil || n.Billing.CycleUpload != 100 || n.Billing.CycleDownload != 200 || n.Billing.CycleID != v.Billing.CycleID {
		t.Fatal("node did not use own contribution", n.Billing, err)
	}
	quota := int64(20)
	edited, err := s.UpdateServerSettings(v.ID, "renamed", nil, &quota, v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Billing.CycleID != v.Billing.CycleID || edited.Billing.CycleUpload != 400 {
		t.Fatal("capacity edit reset cycle")
	}
	if _, err = s.UpdateServerSettings(v.ID, v.Name, nil, nil, edited.Revision, ServerBillingSetup{ExpiresAt: &expires}); !errors.Is(err, ErrConflict) {
		t.Fatal("settings bypassed renewal history", err)
	}
	history, err := s.ServerRenewals(v.ID)
	if err != nil || len(history) != 1 || history[0].Actor != "billing-admin" || history[0].Kind != "initial" {
		t.Fatal("initial history missing", history, err)
	}
	// Deletion leaves historical association; a replacement node has its own ID.
	if _, err = s.DB.Exec("UPDATE nodes SET lifecycle='deleted',managed=0,deleted_at=? WHERE id=?", now.Unix(), a.ID); err != nil {
		t.Fatal(err)
	}
	late := model.AgentReport{Revision: 1, CoreHealthy: false, Daily: []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(now.Add(-24 * time.Hour)), Upload: 110, Download: 220}}}
	if err = s.Report(a.ID, late); err != nil {
		t.Fatal(err)
	}
	if err = s.Report(a.ID, late); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil || v.Billing.CycleUpload != 410 || v.Billing.CycleDownload != 620 {
		t.Fatal("deleted node late usage lost or duplicated", v.Billing, err)
	}
	rebuilt, err := s.CreateNode(model.Node{Name: "readded same IP", ServerID: v.ID, AddressID: a.AddressID, Domain: "replacement.example.com", Port: 443}, "ssh")
	if err != nil {
		t.Fatal(err)
	}
	rawBillingReport(t, s, rebuilt.ID, *now, 5, 7)
	v, err = s.Server(v.ID)
	if err != nil || rebuilt.ID == a.ID || v.Billing.CycleUpload != 415 || v.Billing.CycleDownload != 627 {
		t.Fatal("readded IP duplicated or dropped old contribution", v.Billing, err)
	}
	if n, err = s.Node(b.ID); err != nil || n.MonthlyQuotaBytes != nil {
		t.Fatal("server quota changed node capacity", err)
	}
}

func TestServerPrepaidRenewalIdempotenceBoundariesAndRestart(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 10, 0, 0, 0, Shanghai)
	v, a, _ := serverPair(t, s)
	expiry := time.Date(2026, 11, 10, 15, 0, 0, 0, Shanghai)
	v = configureBilling(t, s, v, expiry)
	rawBillingReport(t, s, a.ID, *now, 100, 200)
	months := 2
	request := ServerRenewalRequest{ExpectedRevision: v.Revision, RequestID: "renew-request-1", Months: &months}
	var wg sync.WaitGroup
	var lock sync.Mutex
	records := map[string]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, r, err := s.RenewServer(v.ID, request, "operator")
			if err != nil {
				t.Error(err)
				return
			}
			lock.Lock()
			records[r.ID] = true
			lock.Unlock()
		}()
	}
	wg.Wait()
	if len(records) != 1 {
		t.Fatal("retry produced multiple renewals")
	}
	v, err := s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Billing.ExpiresAt.Equal(expiry.Add(60*24*time.Hour)) || v.Billing.CycleUpload != 100 || v.Billing.NextResetAt == nil || !v.Billing.NextResetAt.Equal(expiry) {
		t.Fatal("advance renewal reset early or moved anchor", v.Billing)
	}
	other := 3
	request.Months = &other
	if _, _, err = s.RenewServer(v.ID, request, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatal("idempotency key accepted different payload", err)
	}
	request.RequestID = "stale-new-request"
	if _, _, err = s.RenewServer(v.ID, request, "operator"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale CAS accepted", err)
	}
	*now = expiry.Add(70 * time.Minute)
	rawBillingReport(t, s, a.ID, *now, 1000, 2000,
		model.UsageBucket{Start: expiry.Add(-time.Hour).Unix(), Upload: 600, Download: 1200},
		model.UsageBucket{Start: expiry.Unix(), Upload: 150, Download: 300},
		model.UsageBucket{Start: expiry.Add(time.Hour).Unix(), Upload: 250, Download: 500})
	v, err = s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Billing.CycleUpload != 400 || v.Billing.CycleDownload != 800 || !v.Billing.CycleStartedAt.Equal(expiry) || !v.Billing.CycleComplete {
		t.Fatal("boundary hours double counted or normal report lag marked incomplete", v.Billing)
	}
	firstCycle := v.Billing.CycleID
	*now = expiry.Add(30 * 24 * time.Hour)
	v, err = s.Server(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Billing.CycleID == firstCycle || v.Billing.NextResetAt != nil || !v.Billing.CycleStartedAt.Equal(*now) {
		t.Fatal("last paid interval boundary wrong", v.Billing)
	}
	finalCycle := v.Billing.CycleID
	*now = expiry.Add(60 * 24 * time.Hour)
	rawBillingReport(t, s, a.ID, *now, 40, 60)
	v, err = s.Server(v.ID)
	if err != nil || v.Billing.Status != "expired" || v.Billing.CycleID != finalCycle || v.Billing.CycleUpload != 40 {
		t.Fatal("final expiry reset usage", v.Billing, err)
	}
	reopened, err := Open(s.DataDir, s.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Now = s.Now
	loaded, err := reopened.Server(v.ID)
	if err != nil || loaded.Billing.CycleID != finalCycle || loaded.Billing.CycleUpload != 40 {
		t.Fatal("restart changed cycle", loaded.Billing, err)
	}
	*now = now.Add(4 * 24 * time.Hour)
	rawBillingReport(t, s, a.ID, *now, 5, 7)
	loaded, err = reopened.Server(v.ID)
	if err != nil || loaded.Billing.CycleUpload != 45 {
		t.Fatal("expired server stopped accumulating", loaded.Billing, err)
	}
}

func TestServerOverdueRenewalExcludesUnresolvedHourAndKeepsHistory(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 9, 0, 0, 0, Shanghai)
	v, a, _ := serverPair(t, s)
	expiry := now.Add(time.Hour)
	v = configureBilling(t, s, v, expiry)
	*now = expiry.Add(30 * time.Minute)
	rawBillingReport(t, s, a.ID, *now, 500, 1000, model.UsageBucket{Start: expiry.Unix(), Upload: 500, Download: 1000})
	months := 1
	if _, _, err := s.RenewServer(v.ID, ServerRenewalRequest{ExpectedRevision: v.Revision, RequestID: "overdue-months", Months: &months}, "admin"); err == nil {
		t.Fatal("overdue inferred a new 30 day expiry")
	}
	newExpiry := time.Date(2026, 12, 20, 12, 0, 0, 0, Shanghai)
	v, r, err := s.RenewServer(v.ID, ServerRenewalRequest{ExpectedRevision: v.Revision, RequestID: "manual-overdue", ExpiresAt: &newExpiry}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "overdue" || v.Billing.CycleUpload != 0 || v.Billing.CycleComplete || !v.Billing.CycleStartedAt.Equal(*now) {
		t.Fatal("mid-hour cutover charged previous traffic", v.Billing)
	}
	*now = expiry.Add(2 * time.Hour)
	rawBillingReport(t, s, a.ID, *now, 530, 1060, model.UsageBucket{Start: expiry.Add(time.Hour).Unix(), Upload: 30, Download: 60})
	v, err = s.Server(v.ID)
	if err != nil || v.Billing.CycleUpload != 30 || v.Billing.CycleDownload != 60 || v.MonthUpload != 530 {
		t.Fatal("new cycle or old month wrong", v.Billing, err)
	}
	before := v.Billing.CycleID
	*now = newExpiry.Add(10 * time.Hour)
	// Lost detail retention cannot erase current cycle's archived opening hour.
	if _, err = s.DB.Exec("DELETE FROM daily;DELETE FROM native_hourly"); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil || v.Billing.CycleID != before || v.Billing.CycleUpload != 30 || v.Billing.Status != "expired" {
		t.Fatal("cache lost retained opening hour", v.Billing, err)
	}
}

func TestServerCycleUnknownHoursDoNotBecomeKnownDailyUsage(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 9, 0, 0, 0, Shanghai)
	v, a, _ := serverPair(t, s)
	v = configureBilling(t, s, v, now.Add(24*time.Hour))
	rawBillingReport(t, s, a.ID, *now, 100, 200)
	*now = now.Add(48 * time.Hour)
	rawBillingReport(t, s, a.ID, *now, 900, 1800, model.UsageBucket{Start: now.Truncate(time.Hour).Unix(), Upload: 800, Download: 1600, Uncertain: true})
	v, err := s.Server(v.ID)
	if err != nil || v.Billing.CycleUpload != 200 || v.Billing.CycleDownload != 400 || v.Billing.CycleComplete {
		t.Fatal("unknown recovered bytes counted as this cycle", v.Billing, err)
	}
	if _, err = s.DB.Exec("DELETE FROM daily;DELETE FROM native_hourly"); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil || v.Billing.CycleUpload != 200 || v.Billing.CycleComplete {
		t.Fatal("pruning lost unknown exclusion", v.Billing, err)
	}
}

func TestServerBillingRenewalDoesNotChangeUserBudgetOrNodeAuthorization(t *testing.T) {
	quota := int64(1024)
	s, now, u, _, n, password := portalFixture(t, 1, &quota)
	v, err := s.Server(n.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	v = configureBilling(t, s, v, now.Add(24*time.Hour).Truncate(time.Hour))
	l := acquire(t, s, n, password, "a")
	before, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	var desiredBefore, desiredAfter int64
	s.DB.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", n.ID).Scan(&desiredBefore)
	months := 1
	if _, _, err = s.RenewServer(v.ID, ServerRenewalRequest{ExpectedRevision: v.Revision, RequestID: "isolation-renew", Months: &months}, "admin"); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", n.ID).Scan(&desiredAfter)
	after, _ := s.UserSubscription(u.ID, u.Subscriptions[0].ID)
	if before.UsedBytes != after.UsedBytes || before.ReservedBytes != after.ReservedBytes || before.Revision != after.Revision || desiredBefore != desiredAfter {
		t.Fatal("hosting renewal changed user enforcement")
	}
	_ = renew(t, s, l, 10, 20, false)
	if _, err = s.AcquireLease(n.ID, strings.Repeat("b", 32), password); !errors.Is(err, ErrSlots) {
		t.Fatal("renewal released user slot", err)
	}
}

func TestServerBillingAggregateOverflowDoesNotWrap(t *testing.T) {
	out := model.ServerBilling{CycleUpload: math.MaxInt64 - 10}
	if err := addBillingUsage(&out, 10, 1); err == nil || out.CycleDownload != 0 {
		t.Fatal("aggregate counter overflow wrapped")
	}
}

func TestServerBillingOldAgentGapKeepsVerifiedDaysAndExcludesAmbiguousDays(t *testing.T) {
	s, now := fixture(t)
	*now = time.Date(2026, 11, 7, 9, 0, 0, 0, Shanghai)
	v, a, _ := serverPair(t, s)
	v = configureBilling(t, s, v, now.Add(30*24*time.Hour))
	rawBillingReport(t, s, a.ID, *now, 100, 200)
	knownDay := *now
	*now = now.Add(24 * time.Hour)
	rawBillingReport(t, s, a.ID, *now, 500, 1000)
	gapStart := now.Add(-time.Hour)
	*now = now.Add(48 * time.Hour)
	rawBillingReport(t, s, a.ID, *now, 700, 1400, model.UsageBucket{Start: now.Truncate(time.Hour).Unix(), Upload: 0, Download: 0, Uncertain: true})
	if _, err := s.DB.Exec("INSERT INTO node_hourly_gaps(node_id,start,end) VALUES(?,?,?)", a.ID, gapStart.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	v, err := s.Server(v.ID)
	if err != nil || v.Billing.CycleUpload != 100 || v.Billing.CycleDownload != 200 || v.Billing.CycleComplete {
		t.Fatal("old Agent gap charged ambiguous days or erased earlier known day", v.Billing, err)
	}
	if _, err = s.DB.Exec("DELETE FROM daily WHERE start>?;DELETE FROM native_hourly", DayStart(knownDay)); err != nil {
		t.Fatal(err)
	}
	v, err = s.Server(v.ID)
	if err != nil || v.Billing.CycleUpload != 100 {
		t.Fatal("gap exclusion did not survive pruning", v.Billing, err)
	}
}

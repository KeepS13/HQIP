package store

import (
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
	"strings"
	"sync"
	"testing"
	"time"
)

func offlineFixture(t *testing.T, slots int, quota *int64) (*Store, *time.Time, model.Node, string) {
	s, now, _, _, n, password := portalFixture(t, slots, quota)
	if err := s.SetOfflineAdmissionEnabled(n.ID, true); err != nil {
		t.Fatal(err)
	}
	return s, now, n, password
}

func TestOfflineGrantConservationReplayAndLateFinal(t *testing.T) {
	quota := int64(64 << 20)
	s, now, n, password := offlineFixture(t, 2, &quota)
	var wg sync.WaitGroup
	results := make(chan model.OfflineGrant, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g, err := s.AcquireOfflineGrant(n.ID, strings.Repeat(string(rune('a'+i)), 64), password)
			if err == nil {
				results <- g
			} else if !errors.Is(err, ErrSlots) {
				t.Errorf("admission: %v", err)
			}
		}(i)
	}
	wg.Wait()
	close(results)
	var grants []model.OfflineGrant
	var normal, emergency int64
	for g := range results {
		grants = append(grants, g)
		normal += g.NormalBudgetBytes
		emergency += g.EmergencyBudgetBytes
	}
	if len(grants) != 2 || normal != quota || emergency != model.EmergencyAllowanceBytes {
		t.Fatalf("reservations: count=%d normal=%d emergency=%d", len(grants), normal, emergency)
	}
	g := grants[0]
	if !g.OfflineUntil.Equal(now.Add(24 * time.Hour)) {
		t.Fatal("wrong original deadline", g.OfflineUntil, *now)
	}
	// Offline consumption uses the normal reservation first, then its portion
	// of the single subscription emergency pool. Report delivery can be lost.
	*now = now.Add(3 * time.Hour)
	u := model.OfflineUsage{Version: 1, DownloadBytes: g.NormalBudgetBytes + 123, EmergencyUsedBytes: 123, AppliedRateVersion: g.RateVersion, Active: true}
	ack, err := s.ReportOfflineGrant(n.ID, g.ID, u)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	replay, err := s.ReportOfflineGrant(n.ID, g.ID, u)
	if err != nil || !replay.AuthorizedAt.Equal(ack.AuthorizedAt) {
		t.Fatal("replay renewed authorization", err)
	}
	u.DownloadBytes++
	if _, err = s.ReportOfflineGrant(n.ID, g.ID, u); !errors.Is(err, ErrConflict) {
		t.Fatal("same-version mismatch accepted", err)
	}
	u.DownloadBytes--
	u.Version = 2
	u.Active = false
	idle, err := s.ReportOfflineGrant(n.ID, g.ID, u)
	if err != nil || !idle.AuthorizedAt.Equal(ack.AuthorizedAt) {
		t.Fatal("idle reservation renewed", err)
	}
	u.Version = 1
	if _, err = s.ReportOfflineGrant(n.ID, g.ID, u); !errors.Is(err, ErrConflict) {
		t.Fatal("out-of-order report accepted", err)
	}
	if err = s.RevokePortalConnection(g.UserID, g.ID, ""); err != nil {
		t.Fatal(err)
	}
	u.Version = 3
	u.Closed = true
	final, err := s.ReportOfflineGrant(n.ID, g.ID, u)
	if err != nil || !final.Closed || !final.Revoked {
		t.Fatal("final resurrected revocation", err)
	}
	if _, err = s.ReportOfflineGrant(n.ID, g.ID, u); err != nil {
		t.Fatal("final replay", err)
	}
	var used, reserved, regularReserved, actual int64
	_ = s.DB.QueryRow("SELECT used,reserved FROM portal_emergency_usage WHERE subscription_id=?", g.SubscriptionID).Scan(&used, &reserved)
	_ = s.DB.QueryRow("SELECT reserved,upload+download FROM portal_usage WHERE subscription_id=?", g.SubscriptionID).Scan(&regularReserved, &actual)
	if used != 123 || reserved != grants[1].EmergencyBudgetBytes || regularReserved != grants[1].NormalBudgetBytes || actual != g.NormalBudgetBytes+123 {
		t.Fatalf("accounting used=%d reserved=%d regular=%d actual=%d", used, reserved, regularReserved, actual)
	}
	// Feature disable preserves settlement, but cannot issue a new reservation.
	_ = s.SetOfflineAdmissionEnabled(n.ID, false)
	if _, err = s.AcquireOfflineGrant(n.ID, strings.Repeat("f", 64), password); err == nil {
		t.Fatal("issuance continued after disable")
	}
}

func TestOfflineGrantDeadlineExpiryAndUnlimited(t *testing.T) {
	s, now, n, password := offlineFixture(t, 2, nil)
	g, err := s.AcquireOfflineGrant(n.ID, strings.Repeat("a", 64), password)
	if err != nil {
		t.Fatal(err)
	}
	if !g.NormalUnlimited || g.NormalBudgetBytes != 0 || g.EmergencyBudgetBytes != 0 {
		t.Fatal("unlimited gained a finite cap")
	}
	var reserved int64
	_ = s.DB.QueryRow("SELECT reserved FROM portal_usage WHERE subscription_id=?", g.SubscriptionID).Scan(&reserved)
	if reserved != 0 {
		t.Fatal("unlimited reserved finite bytes")
	}
	*now = g.OfflineUntil
	u := model.OfflineUsage{Version: 1, DownloadBytes: 1 << 30, Active: true, AppliedRateVersion: g.RateVersion}
	ack, err := s.ReportOfflineGrant(n.ID, g.ID, u)
	if err != nil || !ack.Revoked || !ack.OfflineUntil.Equal(g.OfflineUntil) {
		t.Fatal("expired permit resurrected", err)
	}
	u.Version++
	u.Active = false
	u.Closed = true
	if _, err = s.ReportOfflineGrant(n.ID, g.ID, u); err != nil {
		t.Fatal("late settlement", err)
	}
	_ = s.DB.QueryRow("SELECT reserved FROM portal_usage WHERE subscription_id=?", g.SubscriptionID).Scan(&reserved)
	if reserved != 0 {
		t.Fatal("unlimited settlement changed reservations")
	}
}

func TestOfflineGrantRejectsOverBudgetWithoutPartialAccounting(t *testing.T) {
	q := int64(8 << 20)
	s, _, n, p := offlineFixture(t, 1, &q)
	g, err := s.AcquireOfflineGrant(n.ID, strings.Repeat("a", 64), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []model.OfflineUsage{{Version: 1, DownloadBytes: q + 1}, {Version: 1, DownloadBytes: q + model.EmergencyAllowanceBytes + 1, EmergencyUsedBytes: model.EmergencyAllowanceBytes + 1}} {
		if _, err = s.ReportOfflineGrant(n.ID, g.ID, u); !errors.Is(err, ErrConflict) {
			t.Fatal("budget violation accepted", err)
		}
	}
	var used int64
	_ = s.DB.QueryRow("SELECT upload+download FROM portal_usage WHERE subscription_id=?", g.SubscriptionID).Scan(&used)
	if used != 0 {
		t.Fatal("rejected report partially billed")
	}
	if _, err = s.RenewLease(n.ID, g.ID, model.LeaseUsage{Closed: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy endpoint bypassed accounting", err)
	}
}

package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestRenewalMigrationPreservesInitialUsageHoldsAndFraction(t *testing.T) {
	s, _, user, _, sub, password := renewalFixture(t)
	lease, err := s.AcquireLease(sub.NodeIDs[0], strings.Repeat("m", 32), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewLease(lease.NodeID, lease.ID, model.LeaseUsage{UploadBytes: 123, DownloadBytes: 456}); err != nil {
		t.Fatal(err)
	}
	before, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the pre-renewal tables while preserving the existing business data.
	for _, query := range []string{"DROP TABLE payment_renewals", "DROP TABLE subscription_lease_periods", "DROP TABLE subscription_periods", "DELETE FROM metadata WHERE key='subscription_renewals_version'"} {
		audienceExec(t, s, query)
	}
	audienceExec(t, s, "INSERT INTO traffic_billing_remainders(subscription_id,upload,download) VALUES(?,25,25)", sub.ID)
	for range 2 {
		if s.Postgres() {
			err = MigratePostgresSubscriptionRenewals(context.Background(), s.postgresDSN)
		} else {
			err = EnsureSubscriptionRenewalSchema(s.DB)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("migration altered entitlement", after, before, err)
	}
	var remainder int64
	if err = s.DB.QueryRow("SELECT remainder FROM subscription_periods WHERE id=?", sub.ID).Scan(&remainder); err != nil || remainder != 50 {
		t.Fatal("lost fractional units", remainder, err)
	}
	period, err := leasePeriod(s.DB, lease.ID)
	if err != nil || period != sub.ID {
		t.Fatal("lease lost original period", period, err)
	}
	if _, err = s.RenewLease(lease.NodeID, lease.ID, model.LeaseUsage{UploadBytes: 223, DownloadBytes: 456, Closed: true}); err != nil {
		t.Fatal(err)
	}
	after, _ = s.UserSubscription(user.ID, sub.ID)
	if after.UsedBytes != 679 || after.ReservedBytes != 0 {
		t.Fatal("migrated lease cannot settle", after)
	}
}

func TestRenewalRecoveryRequiresExplicitHistoricalAttribution(t *testing.T) {
	s, now, user, template, sub, _ := renewalFixture(t)
	paidRenewal(t, s, template, sub, "before-recovery")
	*now = sub.ExpiresAt.Add(time.Minute)
	if err := s.ReconcileRestore(); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(121 * time.Second)
	audienceExec(t, s, "UPDATE nodes SET last_seen=?,applied_revision=desired_revision,core_healthy=1", now.Unix())
	state, err := s.PortalRecovery()
	if err != nil || !state.Ready {
		t.Fatal(state, err)
	}
	totals := []RecoveryTotal{}
	for _, entry := range state.Users {
		total := RecoveryTotal{SubscriptionID: entry.SubscriptionID, UserID: entry.UserID, UsedBytes: entry.MinimumUsedBytes}
		if entry.SubscriptionID == sub.ID {
			total.UsedBytes += 200
		}
		totals = append(totals, total)
	}
	if err = s.ResumePortalRecovery(state.Epoch, true, totals); err == nil {
		t.Fatal("unattributed lifetime correction charged current period")
	}
	for i := range totals {
		if totals[i].SubscriptionID == sub.ID {
			totals[i].PeriodAdjustments = []RecoveryPeriodAdjustment{{PeriodID: sub.ID, AdditionalUsedBytes: 200}}
		}
	}
	if err = s.ResumePortalRecovery(state.Epoch, true, totals); err != nil {
		t.Fatal(err)
	}
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || after.UsedBytes != 0 || after.Periods[0].DownloadBytes != 200 {
		t.Fatal("recovery changed new period", after, err)
	}
}

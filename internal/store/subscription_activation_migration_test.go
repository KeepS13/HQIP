package store

import (
	"context"
	"reflect"
	"testing"
)

func TestActivationMigrationPreservesExistingPeriods(t *testing.T) {
	s, _, user, template, sub, _ := renewalFixture(t)
	paidRenewal(t, s, template, sub, "before-activation-upgrade")
	before, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	audienceExec(t, s, "DROP TABLE subscription_provisioning")
	audienceExec(t, s, "UPDATE metadata SET value='1' WHERE key='subscription_renewals_version'")
	if s.Postgres() {
		if old, err := reopenFixture(s); err == nil {
			old.Close()
			t.Fatal("old schema was silently accepted")
		}
	}
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
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed existing rights", before, after, err)
	}
	var version string
	if err = s.DB.QueryRow("SELECT value FROM metadata WHERE key='subscription_renewals_version'").Scan(&version); err != nil || version != "2" {
		t.Fatal(version, err)
	}
}

func TestPendingActivationSurvivesReopenAndRecovery(t *testing.T) {
	s, _, user, template := allocationFixture(t, 0, 1)
	sub := assignAllocation(t, s, user.ID, template)
	other, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.Now = s.Now
	if err = other.ReconcileRestore(); err != nil {
		t.Fatal(err)
	}
	got, err := other.UserSubscription(user.ID, sub.ID)
	if err != nil || !got.ActivationPending || !got.ExpiresAt.IsZero() || len(got.Periods) != 0 {
		t.Fatal(got, err)
	}
}

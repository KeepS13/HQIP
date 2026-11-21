package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestRenewalSnapshotRestoreKeepsPeriodsAndLateSettlement(t *testing.T) {
	s, now, user, template, sub, password := renewalFixture(t)
	if s.Postgres() {
		t.Skip("SQLite snapshot round trip; PostgreSQL archives require pg_dump/pg_restore")
	}
	*now = sub.ExpiresAt.Add(-30 * time.Second)
	lease, err := s.AcquireLease(sub.NodeIDs[0], strings.Repeat("s", 32), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewLease(lease.NodeID, lease.ID, model.LeaseUsage{UploadBytes: 120}); err != nil {
		t.Fatal(err)
	}
	order := paidRenewal(t, s, template, sub, "snapshot-renewal")
	before, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = s.Snapshot(context.Background(), filepath.Join(dir, "hqip.db")); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(s.DataDir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(dir, s.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restored.Now = s.Now
	if err = restored.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
	if err = restored.ReconcileRestore(); err != nil {
		t.Fatal(err)
	}
	after, err := restored.UserSubscription(user.ID, sub.ID)
	if err != nil || !reflect.DeepEqual(before.Periods, after.Periods) || after.UsedBytes != before.UsedBytes || after.ReservedBytes != before.ReservedBytes {
		t.Fatal(after, err)
	}
	got, err := restored.PaymentOrder(user.ID, order.ID)
	if err != nil || got.Status != "fulfilled" || !got.PeriodStartsAt.Equal(*order.PeriodStartsAt) {
		t.Fatal(got, err)
	}
	*now = sub.ExpiresAt
	if _, err = restored.RenewLease(lease.NodeID, lease.ID, model.LeaseUsage{UploadBytes: 140, Closed: true}); err != nil {
		t.Fatal(err)
	}
	after, err = restored.UserSubscription(user.ID, sub.ID)
	if err != nil || after.UsedBytes != 0 || after.ReservedBytes != 0 || after.Periods[0].UploadBytes != 140 || after.Periods[0].ReservedBytes != 0 {
		t.Fatal(after, err)
	}
}

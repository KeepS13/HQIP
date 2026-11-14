package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresReportCancellationRollsBackAndCanReplay(t *testing.T) {
	s, now := fixture(t)
	if !s.Postgres() {
		t.Skip("PostgreSQL integration environment not configured")
	}
	n := newNode(t, s, "cancel-report")
	d := newDevice(t, s, "cancel-device")
	desired, err := s.Desired(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := model.AgentReport{Revision: desired.Revision,
		Daily:   []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(*now), Upload: 100, Download: 200}},
		Minutes: []model.UsageBucket{{DeviceID: d.ID, Start: now.Unix() / 60 * 60, Upload: 100, Download: 200}},
	}
	blocker, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.Exec("LOCK TABLE minute IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = s.ReportContext(ctx, n.ID, r)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("disconnected report kept doing database work: %v (%s)", err, time.Since(started))
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM lifetime WHERE node_id=?", n.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled report partially committed", count, err)
	}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal("cancelled report could not be replayed", err)
	}
	var up, down int64
	if err := s.DB.QueryRow("SELECT upload,download FROM lifetime WHERE node_id=? AND device_id=?", n.ID, d.ID).Scan(&up, &down); err != nil || up != 100 || down != 200 {
		t.Fatal("replay changed totals", up, down, err)
	}
}

func TestPostgresNativeHourReplayDoesNotRewriteUnchangedBuckets(t *testing.T) {
	s, now := fixture(t)
	if !s.Postgres() {
		t.Skip("PostgreSQL integration environment not configured")
	}
	n := newNode(t, s, "native-replay")
	d := newDevice(t, s, "native-device")
	r := hourlyReportFor(t, s, n, d, *now)
	r.Hours = []model.UsageBucket{
		{DeviceID: d.ID, Start: now.Truncate(time.Hour).Unix(), Upload: 100, Download: 200},
		{DeviceID: d.ID, Start: now.Truncate(time.Hour).Unix(), Upload: 90, Download: 220, Uncertain: true},
	}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	config, err := pgstore.Config(s.postgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	raw := stdlib.OpenDB(*config)
	defer raw.Close()
	// Any repeat UPDATE fails, even if it would write identical values. This
	// verifies replay avoids WAL/index work, not just unchanged final totals.
	for _, statement := range []string{
		`CREATE FUNCTION reject_hour_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unchanged hour rewritten'; END $$`,
		`CREATE TRIGGER reject_hour_rewrite BEFORE UPDATE ON native_hourly FOR EACH ROW EXECUTE FUNCTION reject_hour_rewrite()`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal("replay rewrote a cumulative hour", err)
	}
	var up, down int64
	var uncertain bool
	if err := s.DB.QueryRow("SELECT upload,download,uncertain FROM native_hourly WHERE node_id=? AND device_id=?", n.ID, d.ID).Scan(&up, &down, &uncertain); err != nil || up != 100 || down != 220 || !uncertain {
		t.Fatal("duplicate hours lost maxima/uncertainty", up, down, uncertain, err)
	}
	// Zero counters still establish an explicit observed bucket.
	zero := model.UsageBucket{DeviceID: d.ID, Start: now.Add(-time.Hour).Truncate(time.Hour).Unix()}
	r.Hours = []model.UsageBucket{zero}
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT upload FROM native_hourly WHERE node_id=? AND device_id=? AND start=?", n.ID, d.ID, zero.Start).Scan(&up); err != nil || up != 0 {
		t.Fatal("zero bucket became missing", up, err)
	}
}

func TestPostgresBatchReportsAcrossPoolsPreserveMaxima(t *testing.T) {
	s, now := fixture(t)
	if !s.Postgres() {
		t.Skip("PostgreSQL integration environment not configured")
	}
	n := newNode(t, s, "batch-concurrency")
	d := newDevice(t, s, "batch-device")
	other, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.Now = s.Now
	desired, err := s.Desired(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const records, workers = 300, 8
	for worker := 1; worker <= workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			db := s
			if worker%2 == 0 {
				db = other
			}
			up, down := int64(worker), int64(workers+1-worker)
			r := model.AgentReport{Revision: desired.Revision, Daily: []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(*now), Upload: up * records, Download: down * records}}}
			for i := 0; i < records; i++ {
				r.Minutes = append(r.Minutes, model.UsageBucket{DeviceID: d.ID, Start: now.Add(-time.Duration(i)*time.Minute).Unix() / 60 * 60, Upload: up, Download: down})
			}
			if err := db.Report(n.ID, r); err != nil {
				t.Error(err)
			}
		}(worker)
	}
	wg.Wait()
	for _, table := range []string{"minute", "hourly", "lifetime"} {
		var up, down int64
		if err := s.DB.QueryRow("SELECT SUM(upload),SUM(download) FROM "+table+" WHERE node_id=?", n.ID).Scan(&up, &down); err != nil || up != workers*records || down != workers*records {
			t.Fatalf("%s concurrency double charged/lost bytes: %d %d %v", table, up, down, err)
		}
	}
}

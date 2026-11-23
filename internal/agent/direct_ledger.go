package agent

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/runtime/nodepolicy"
	"github.com/KeepS13/HQIP/runtime/nodepolicy/billing"
)

// recordDirect feeds existing raw daily/minute/hourly server statistics. A
// private baseline key isolates each direct lease from HY2's accounting-ID
// counters; both add to the same business buckets. The transaction advances
// usage and baseline together, so retries never charge server traffic twice.
// Subscription billing remains exclusively owned by cumulative lease reports.
func (l *ledger) recordDirect(ctx context.Context, now time.Time, snapshots []nodepolicy.Snapshot) (int64, int64, error) {
	if now.UnixNano() <= 0 {
		return 0, 0, errors.New("invalid direct sample time")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	// Direct-only nodes can establish coverage. Once HY2 has sampled, its
	// watermark must remain intact: beginHourlySample uses it to find counters
	// lost during a core restart. Known direct buckets are still reported beyond
	// that watermark, without claiming completeness for the whole node.
	_, err = tx.ExecContext(ctx, `INSERT INTO hourly_state(id,since,through,generation) VALUES(1,?,?,'direct') ON CONFLICT(id) DO NOTHING`, now.UnixNano(), now.UnixNano())
	if err != nil {
		return 0, 0, err
	}
	var through int64
	var generation string
	if err = tx.QueryRowContext(ctx, "SELECT through,generation FROM hourly_state WHERE id=1").Scan(&through, &generation); err != nil {
		return 0, 0, err
	}
	if now.UnixNano() < through {
		return 0, 0, errors.New("direct sample clock regressed")
	}
	if generation == "direct" {
		through = now.UnixNano()
	}
	var up, down int64
	for _, s := range snapshots {
		if s.ID == "" || s.AccountingID == "" || s.Upload < 0 || s.Download < 0 || s.Upload > hourlyCounterLimit || s.Download > hourlyCounterLimit {
			return 0, 0, errors.New("invalid direct telemetry")
		}
		key := "direct:" + s.ID
		var old counter
		from := now.UnixNano()
		err = tx.QueryRowContext(ctx, "SELECT tx,rx,sampled_at FROM baseline WHERE device_id=?", key).Scan(&old.TX, &old.RX, &from)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, err
		}
		first := errors.Is(err, sql.ErrNoRows)
		if from > now.UnixNano() {
			return 0, 0, errors.New("direct sample clock regressed")
		}
		if s.Upload < old.TX || s.Download < old.RX {
			return 0, 0, errors.New("direct telemetry regressed")
		}
		delta := counter{TX: s.Upload - old.TX, RX: s.Download - old.RX}
		hourlyFrom := from
		uncertain := first || now.UnixNano()-from > int64(5*time.Second)
		if now.UnixNano()-from > int64(24*time.Hour) {
			// Preserve exact totals without inventing unbounded hourly detail.
			// Never narrow since beyond the shared completeness watermark.
			if err = narrowHourlyCoverage(ctx, tx, through); err != nil {
				return 0, 0, err
			}
			hourlyFrom = now.UnixNano()
		}
		if delta.TX != 0 || delta.RX != 0 {
			if err = addUsage(ctx, tx, s.AccountingID, from, now, delta); err != nil {
				return 0, 0, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO totals(device_id,upload,download) VALUES(?,?,?) ON CONFLICT(device_id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download`, s.AccountingID, delta.TX, delta.RX)
			if err != nil {
				return 0, 0, err
			}
		}
		if delta.TX != 0 || delta.RX != 0 || (!first && uncertain) {
			if err = addHourlyUsage(ctx, tx, s.AccountingID, hourlyFrom, now, delta, uncertain); err != nil {
				return 0, 0, err
			}
		}
		if s.Closed {
			_, err = tx.ExecContext(ctx, "DELETE FROM baseline WHERE device_id=?", key)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO baseline(device_id,generation,tx,rx,sampled_at) VALUES(?,'direct',?,?,?) ON CONFLICT(device_id) DO UPDATE SET tx=excluded.tx,rx=excluded.rx,sampled_at=excluded.sampled_at`, key, s.Upload, s.Download, now.UnixNano())
		}
		if err != nil {
			return 0, 0, err
		}
		if up, err = billing.Add(up, delta.TX); err != nil {
			return 0, 0, err
		}
		if down, err = billing.Add(down, delta.RX); err != nil {
			return 0, 0, err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE hourly_state SET through=? WHERE id=1 AND generation='direct'", through)
	if err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return up, down, nil
}

package agent

import (
	"context"
	"database/sql"
	"errors"
	"math/bits"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

const hourlyCounterLimit = int64(1 << 60)

type hourlySample struct {
	since      int64
	through    int64
	generation string
	first      bool
}

func ensureHourlyLedger(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS native_hourly (
 start INTEGER NOT NULL,device_id TEXT NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,
 uncertain INTEGER NOT NULL DEFAULT 0,dirty INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(start,device_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS native_hourly_dirty ON native_hourly(dirty,start,device_id);
CREATE TABLE IF NOT EXISTS native_baseline (
 device_id TEXT PRIMARY KEY,generation TEXT NOT NULL,tx INTEGER NOT NULL,rx INTEGER NOT NULL,sampled_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS hourly_state (
 id INTEGER PRIMARY KEY CHECK(id=1),since INTEGER NOT NULL,through INTEGER NOT NULL,generation TEXT NOT NULL);`)
	return err
}

// Coverage begins at the first supported sample, not at an older minute/day
// bucket. Those counters may already include traffic from before the upgrade.
func beginHourlySample(ctx context.Context, tx *sql.Tx, now time.Time, generation string, counters map[string]counter) (hourlySample, error) {
	var s hourlySample
	if len(counters) > reportBatchSize || now.UnixNano() <= 0 {
		return s, errors.New("小时采样时间或计量对象数量无效")
	}
	err := tx.QueryRowContext(ctx, "SELECT since,through,generation FROM hourly_state WHERE id=1").Scan(&s.since, &s.through, &s.generation)
	if errors.Is(err, sql.ErrNoRows) {
		s.first = true
		s.since, s.through = now.UnixNano(), now.UnixNano()
		_, err = tx.ExecContext(ctx, "INSERT INTO hourly_state(id,since,through,generation) VALUES(1,?,?,?)", s.since, s.through, generation)
		return s, err
	}
	if err != nil {
		return s, err
	}
	if now.UnixNano() < s.through {
		return s, errors.New("采样时钟回退，等待时钟恢复后继续计量")
	}
	if now.UnixNano()-s.through > int64(24*time.Hour) {
		// A long gap is not a uniform stream of bytes. Keep the daily total but
		// explicitly narrow coverage rather than inventing thousands of hours.
		if err := narrowHourlyCoverage(ctx, tx, now.UnixNano()); err != nil {
			return s, err
		}
		return s, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT device_id FROM native_baseline WHERE generation=? AND sampled_at=?", s.generation, s.through)
	if err != nil {
		return s, err
	}
	missing := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return s, err
		}
		if _, exists := counters[id]; !exists || generation != s.generation {
			missing = append(missing, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	for _, id := range missing {
		// A counter that disappeared may have a missing last sample. A zero
		// uncertain row prevents this interval being displayed as verified zero.
		if err := addHourlyUsage(ctx, tx, id, max(s.through, s.since), now, counter{}, true); err != nil {
			return s, err
		}
	}
	return s, nil
}

func recordHourlyCounter(ctx context.Context, tx *sql.Tx, s hourlySample, now time.Time, generation, id, oldGeneration string, oldAt int64, old, current, delta counter) error {
	// The old Agent may continue updating the shared daily/minute baseline
	// during a rollback. Native accounting needs its own baseline so upgrading
	// again does not silently drop those bytes or charge daily traffic twice.
	var nativeGeneration string
	var nativeAt int64
	var native counter
	err := tx.QueryRowContext(ctx, "SELECT generation,tx,rx,sampled_at FROM native_baseline WHERE device_id=?", id).Scan(&nativeGeneration, &native.TX, &native.RX, &nativeAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rolledBack := err == nil && oldAt > nativeAt
	if err == nil {
		oldGeneration, oldAt, old = nativeGeneration, nativeAt, native
		delta = current
		if nativeGeneration == generation {
			if current.TX >= native.TX {
				delta.TX -= native.TX
			}
			if current.RX >= native.RX {
				delta.RX -= native.RX
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO native_baseline(device_id,generation,tx,rx,sampled_at) VALUES(?,?,?,?,?)
ON CONFLICT(device_id) DO UPDATE SET generation=excluded.generation,tx=excluded.tx,rx=excluded.rx,sampled_at=excluded.sampled_at`, id, generation, current.TX, current.RX, now.UnixNano()); err != nil {
		return err
	}
	if rolledBack {
		if err := narrowHourlyCoverage(ctx, tx, now.UnixNano()); err != nil {
			return err
		}
	}
	if s.first {
		if delta.TX == 0 && delta.RX == 0 {
			return nil
		}
		return addHourlyUsage(ctx, tx, id, now.UnixNano(), now, delta, true)
	}
	from := max(s.through, s.since)
	uncertain := rolledBack || generation != s.generation || now.UnixNano()-s.through > int64(5*time.Second)
	if oldGeneration == generation {
		from = oldAt
		uncertain = uncertain || current.TX < old.TX || current.RX < old.RX
		if oldAt < s.since {
			// Keep the same unknown amount as the daily ledger, but do not
			// pretend it can be split into pre/post-coverage traffic.
			from = now.UnixNano()
			uncertain = true
		}
	}
	if now.UnixNano()-from > int64(24*time.Hour) {
		if err := narrowHourlyCoverage(ctx, tx, now.UnixNano()); err != nil {
			return err
		}
		from = now.UnixNano()
		uncertain = true
	}
	uncertain = uncertain || now.UnixNano()-from > int64(5*time.Second) || from > now.UnixNano()
	if delta.TX == 0 && delta.RX == 0 && !uncertain {
		return nil
	}
	return addHourlyUsage(ctx, tx, id, from, now, delta, uncertain)
}

func finishHourlySample(ctx context.Context, tx *sql.Tx, now time.Time, generation string) error {
	_, err := tx.ExecContext(ctx, "UPDATE hourly_state SET through=?,generation=? WHERE id=1", now.UnixNano(), generation)
	return err
}

func narrowHourlyCoverage(ctx context.Context, tx *sql.Tx, since int64) error {
	if _, err := tx.ExecContext(ctx, "UPDATE hourly_state SET since=MAX(since,?) WHERE id=1", since); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('compacted','1') ON CONFLICT(key) DO UPDATE SET value='1'")
	return err
}

// Hour allocation is independent of minute retention. Integer division keeps
// exact byte sums, including at midnight and with counters above float precision.
func addHourlyUsage(ctx context.Context, tx *sql.Tx, id string, from int64, now time.Time, delta counter, uncertain bool) error {
	if delta.TX < 0 || delta.RX < 0 || delta.TX > hourlyCounterLimit || delta.RX > hourlyCounterLimit {
		return errors.New("小时计数超出范围")
	}
	end := now.UnixNano()
	if from >= end || from <= 0 {
		from, end = end, end+1
	}
	remaining := delta
	remainingNS := end - from
	for from < end {
		at := time.Unix(0, from).Truncate(time.Hour)
		boundary := min(at.Add(time.Hour).UnixNano(), end)
		part := remaining
		if boundary < end {
			part.TX = hourlyFraction(remaining.TX, boundary-from, remainingNS)
			part.RX = hourlyFraction(remaining.RX, boundary-from, remainingNS)
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO native_hourly(start,device_id,upload,download,uncertain,dirty) VALUES(?,?,?,?,?,1)
ON CONFLICT(start,device_id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download,
uncertain=MAX(uncertain,excluded.uncertain),dirty=1 WHERE upload<=?-excluded.upload AND download<=?-excluded.download`, at.Unix(), id, part.TX, part.RX, uncertain, hourlyCounterLimit, hourlyCounterLimit)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errors.New("小时累计计数超出范围")
		}
		remaining.TX -= part.TX
		remaining.RX -= part.RX
		remainingNS -= boundary - from
		from = boundary
	}
	return nil
}

func hourlyFraction(value, span, total int64) int64 {
	hi, lo := bits.Mul64(uint64(value), uint64(span))
	quotient, _ := bits.Div64(hi, lo, uint64(total))
	return int64(quotient)
}

// The report carries at most one batch, so coverage must stop before an hour
// whose other identities remain queued. A retry repeats the same cumulative
// counters; a concurrent sample cannot be cleared by the later acknowledgment.
func (l *ledger) hourlyBatch(b *reportBatch) error {
	b.hours = []model.UsageBucket{}
	var since, through int64
	err := l.db.QueryRow("SELECT since,through FROM hourly_state WHERE id=1").Scan(&since, &through)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := l.db.Query("SELECT start,device_id,upload,download,uncertain FROM native_hourly WHERE dirty=1 ORDER BY start,device_id LIMIT ?", reportBatchSize+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket model.UsageBucket
		if err := rows.Scan(&bucket.Start, &bucket.DeviceID, &bucket.Upload, &bucket.Download, &bucket.Uncertain); err != nil {
			return err
		}
		if len(b.hours) == reportBatchSize {
			through = max(since, min(through, time.Unix(bucket.Start, 0).UnixNano()))
			// Daily totals must not arrive before the corresponding uncertainty
			// markers. Hold the unfinished day (and all later days) for another
			// batch; this does not change the durable authoritative daily total.
			ready := b.daily[:0]
			for _, day := range b.daily {
				if day.Start+86400 <= bucket.Start {
					ready = append(ready, day)
				}
			}
			b.daily = ready
			break
		}
		b.hours = append(b.hours, bucket)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	start, end := time.Unix(0, since).UTC(), time.Unix(0, through).UTC()
	b.hourlySince, b.hourlyThrough = &start, &end
	return nil
}

func acknowledgeHours(tx *sql.Tx, buckets []model.UsageBucket) error {
	for _, b := range buckets {
		if _, err := tx.Exec("UPDATE native_hourly SET dirty=0 WHERE start=? AND device_id=? AND upload=? AND download=? AND uncertain=?", b.Start, b.DeviceID, b.Upload, b.Download, b.Uncertain); err != nil {
			return err
		}
	}
	return nil
}

func maintainHours(tx *sql.Tx, now time.Time, limitBytes int64) error {
	cutoff := now.AddDate(0, -13, 0).Truncate(time.Hour).Unix()
	var count int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM native_hourly WHERE start>=?", cutoff).Scan(&count); err != nil {
		return err
	}
	rowLimit := max(int64(0), limitBytes/pendingRowBytes)
	if count > rowLimit {
		var last int64
		if err := tx.QueryRow("SELECT start FROM native_hourly WHERE start>=? ORDER BY start,device_id LIMIT 1 OFFSET ?", cutoff, count-rowLimit-1).Scan(&last); err != nil {
			return err
		}
		// Trim complete hours, not an arbitrary subset of identities in an hour.
		cutoff = last + 3600
	}
	var removed, pending int64
	if err := tx.QueryRow("SELECT COUNT(*),COALESCE(SUM(dirty),0) FROM native_hourly WHERE start<?", cutoff).Scan(&removed, &pending); err != nil {
		return err
	}
	if removed == 0 {
		return nil
	}
	if _, err := tx.Exec("DELETE FROM native_hourly WHERE start<?", cutoff); err != nil {
		return err
	}
	// Even an acknowledged interval becomes unrecoverable from this Agent after
	// it is trimmed, so restoring an older control backup must show that gap.
	if _, err := tx.Exec("UPDATE hourly_state SET since=MAX(since,MIN(through,?)) WHERE id=1", time.Unix(cutoff, 0).UnixNano()); err != nil {
		return err
	}
	if pending > 0 || count > rowLimit {
		_, err := tx.Exec("INSERT INTO meta(key,value) VALUES('compacted','1') ON CONFLICT(key) DO UPDATE SET value='1'")
		return err
	}
	return nil
}

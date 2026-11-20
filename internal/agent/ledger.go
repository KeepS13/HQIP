package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	_ "modernc.org/sqlite"
)

const (
	maxPendingBytes = 64 << 20
	// The bound deliberately exceeds the maximum serialized size of one bucket.
	pendingRowBytes = 256
	reportBatchSize = 4000
	// Bound history pages while retaining the protocol's 4000-identity limit.
	reportPageSize = 500
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

type counter struct {
	TX int64 `json:"tx"`
	RX int64 `json:"rx"`
}

type ledger struct {
	mu sync.Mutex
	db *sql.DB
}

type reportBatch struct {
	daily         []model.UsageBucket
	minutes       []model.UsageBucket
	hours         []model.UsageBucket
	hourlySince   *time.Time
	hourlyThrough *time.Time
	compacted     bool
}

func openLedger(path string) (*ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, errors.New("Agent 状态路径必须是普通文件")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	l := &ledger{db: db}
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
PRAGMA busy_timeout=5000;
PRAGMA wal_autocheckpoint=256;
PRAGMA journal_size_limit=1048576;
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS offline_grants(id TEXT PRIMARY KEY,state TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS offline_requests(hash TEXT PRIMARY KEY,request_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS lease_billing_reports(lease_id TEXT PRIMARY KEY,body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS baseline (
 device_id TEXT PRIMARY KEY, generation TEXT NOT NULL, tx INTEGER NOT NULL,
 rx INTEGER NOT NULL, sampled_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS usage (
 kind TEXT NOT NULL, start INTEGER NOT NULL, device_id TEXT NOT NULL,
 upload INTEGER NOT NULL, download INTEGER NOT NULL, dirty INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(kind,start,device_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS usage_dirty ON usage(dirty,kind,start);
CREATE TABLE IF NOT EXISTS totals (
 device_id TEXT PRIMARY KEY, upload INTEGER NOT NULL, download INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS lease_usage (
 lease_id TEXT PRIMARY KEY, upload INTEGER NOT NULL, download INTEGER NOT NULL,
 closed INTEGER NOT NULL, applied_rate INTEGER NOT NULL, version INTEGER NOT NULL,
 dirty INTEGER NOT NULL, received INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt INTEGER NOT NULL DEFAULT 0);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureHourlyLedger(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Replaying retained cumulative counters repairs a control database restored
	// from an older backup. MAX upserts make this harmless after an ordinary restart.
	if err := l.replay(time.Now()); err != nil {
		_ = db.Close()
		return nil, err
	}
	// SQLite inherits database permissions for its WAL and shared-memory files.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0600); err != nil && !os.IsNotExist(err) {
			_ = db.Close()
			return nil, err
		}
	}
	return l, nil
}

func (l *ledger) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, checkpointErr := l.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return errors.Join(checkpointErr, l.db.Close())
}

func (l *ledger) loadCache() (cachedState, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := cachedState{Credentials: make(map[string]cachedCredential), Pending: make(map[string]pendingRevocation)}
	var encoded string
	err := l.db.QueryRow("SELECT value FROM meta WHERE key='credentials'").Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return state, fmt.Errorf("读取鉴权缓存失败: %w", err)
	}
	if state.Credentials == nil {
		state.Credentials = make(map[string]cachedCredential)
	}
	if state.Pending == nil {
		state.Pending = make(map[string]pendingRevocation)
	}
	if state.Removals == nil {
		state.Removals = make(map[string]cachedRemoval)
	}
	if state.DeniedHashes == nil {
		state.DeniedHashes = make(map[string]bool)
	}
	return state, nil
}

func (l *ledger) saveCache(state cachedState) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = l.db.Exec("INSERT INTO meta(key,value) VALUES('credentials',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(encoded))
	return err
}

// record commits usage and the corresponding Hysteria baseline atomically. Keeping
// the systemd invocation with the baseline prevents both agent restart duplicates
// and missed first samples after a Hysteria restart.
func (l *ledger) record(ctx context.Context, now time.Time, generation string, counters map[string]counter) (int64, int64, error) {
	if generation == "" {
		return 0, 0, errors.New("缺少 Hysteria 进程运行代次")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	hourly, err := beginHourlySample(ctx, tx, now, generation, counters)
	if err != nil {
		return 0, 0, err
	}
	var totalTX, totalRX int64
	for id, current := range counters {
		if len(id) == 0 || len(id) > 128 || current.TX < 0 || current.RX < 0 || current.TX > math.MaxInt64/2 || current.RX > math.MaxInt64/2 {
			return 0, 0, errors.New("统计接口返回无效计数")
		}
		var old counter
		var oldGeneration string
		var oldAt int64
		err := tx.QueryRowContext(ctx, "SELECT generation,tx,rx,sampled_at FROM baseline WHERE device_id=?", id).Scan(&oldGeneration, &old.TX, &old.RX, &oldAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, err
		}
		delta := current
		from := now.UnixNano()
		if err == nil && oldGeneration == generation {
			// A reset in the same invocation can only come from another tool clearing
			// the stats API. Treat the new counter as new traffic without underflow.
			if current.TX >= old.TX {
				delta.TX -= old.TX
			}
			if current.RX >= old.RX {
				delta.RX -= old.RX
			}
			from = oldAt
		}
		if err := recordHourlyCounter(ctx, tx, hourly, now, generation, id, oldGeneration, oldAt, old, current, delta); err != nil {
			return 0, 0, err
		}
		if delta.TX != 0 || delta.RX != 0 {
			if err := addUsage(ctx, tx, id, from, now, delta); err != nil {
				return 0, 0, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO totals(device_id,upload,download) VALUES(?,?,?)
ON CONFLICT(device_id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download`, id, delta.TX, delta.RX); err != nil {
				return 0, 0, err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO baseline(device_id,generation,tx,rx,sampled_at) VALUES(?,?,?,?,?)
ON CONFLICT(device_id) DO UPDATE SET generation=excluded.generation,tx=excluded.tx,rx=excluded.rx,sampled_at=excluded.sampled_at`, id, generation, current.TX, current.RX, now.UnixNano()); err != nil {
			return 0, 0, err
		}
		if delta.TX > math.MaxInt64-totalTX || delta.RX > math.MaxInt64-totalRX {
			return 0, 0, errors.New("统计采样总量溢出")
		}
		totalTX += delta.TX
		totalRX += delta.RX
	}
	if err := finishHourlySample(ctx, tx, now, generation); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return totalTX, totalRX, nil
}

// The sample immediately after a short outage may straddle a bucket boundary.
// Allocate it over the observed interval so a midnight tick does not put the
// entire interval into the new billing month. No proxy payloads are inspected.
func addUsage(ctx context.Context, tx *sql.Tx, id string, from int64, now time.Time, delta counter) error {
	end := now.UnixNano()
	if from >= end || from <= 0 {
		from = end
		end++
	}
	// Very long agent downtime cannot be reconstructed at minute resolution.
	// Preserve the total in today's day/minute and surface that detail was lost.
	if end-from > int64(24*time.Hour) {
		from = end
		end++
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('compacted','1') ON CONFLICT(key) DO UPDATE SET value='1'"); err != nil {
			return err
		}
	}
	remaining := delta
	remainingNS := end - from
	for from < end {
		at := time.Unix(0, from)
		boundary := at.Truncate(time.Minute).Add(time.Minute).UnixNano()
		if boundary > end {
			boundary = end
		}
		part := remaining
		if boundary < end {
			fraction := float64(boundary-from) / float64(remainingNS)
			part.TX = int64(float64(remaining.TX) * fraction)
			part.RX = int64(float64(remaining.RX) * fraction)
		}
		minute := at.Truncate(time.Minute).Unix()
		local := at.In(shanghai)
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, shanghai).Unix()
		for kind, start := range map[string]int64{"minute": minute, "day": day} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage(kind,start,device_id,upload,download,dirty) VALUES(?,?,?,?,?,1)
ON CONFLICT(kind,start,device_id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download,dirty=1`, kind, start, id, part.TX, part.RX); err != nil {
				return err
			}
		}
		remaining.TX -= part.TX
		remaining.RX -= part.RX
		remainingNS -= boundary - from
		from = boundary
	}
	return nil
}

func (l *ledger) batch() (reportBatch, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := reportBatch{daily: []model.UsageBucket{}, minutes: []model.UsageBucket{}}
	// Keep the existing daily capacity and reserve a separate minute page. A
	// large set of continuously dirty daily totals must not starve minute data.
	rows, err := l.db.Query(`SELECT kind,start,device_id,upload,download FROM
(SELECT kind,start,device_id,upload,download FROM usage WHERE dirty=1 AND kind='day' ORDER BY start,device_id LIMIT ?)
UNION ALL SELECT kind,start,device_id,upload,download FROM
(SELECT kind,start,device_id,upload,download FROM usage WHERE dirty=1 AND kind='minute' ORDER BY start,device_id LIMIT ?)`, reportBatchSize, reportPageSize)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var kind string
		var bucket model.UsageBucket
		if err := rows.Scan(&kind, &bucket.Start, &bucket.DeviceID, &bucket.Upload, &bucket.Download); err != nil {
			_ = rows.Close()
			return b, err
		}
		if kind == "day" {
			b.daily = append(b.daily, bucket)
		} else {
			b.minutes = append(b.minutes, bucket)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return b, err
	}
	if err := rows.Close(); err != nil {
		return b, err
	}
	var compacted string
	err = l.db.QueryRow("SELECT value FROM meta WHERE key='compacted'").Scan(&compacted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	b.compacted = compacted == "1"
	if err := l.hourlyBatch(&b); err != nil {
		return b, err
	}
	return b, nil
}

func (l *ledger) acknowledge(b reportBatch) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for kind, buckets := range map[string][]model.UsageBucket{"day": b.daily, "minute": b.minutes} {
		for _, bucket := range buckets {
			// Samples that arrived while the HTTP request was in flight stay dirty.
			if _, err := tx.Exec("UPDATE usage SET dirty=0 WHERE kind=? AND start=? AND device_id=? AND upload=? AND download=?", kind, bucket.Start, bucket.DeviceID, bucket.Upload, bucket.Download); err != nil {
				return err
			}
		}
	}
	if err := acknowledgeHours(tx, b.hours); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *ledger) replay(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replayTx(tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func replayTx(tx *sql.Tx, now time.Time) error {
	if _, err := tx.Exec("UPDATE usage SET dirty=1 WHERE (kind='day' AND start>=?) OR (kind='minute' AND start>=?)", now.In(shanghai).AddDate(0, -13, 0).Unix(), now.AddDate(0, 0, -7).Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE native_hourly SET dirty=1 WHERE start>=?", now.AddDate(0, -13, 0).Truncate(time.Hour).Unix()); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO meta(key,value) VALUES('last_replay',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", now.Unix())
	return err
}

func (l *ledger) maintain(now time.Time, queueLimit int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lastReplay int64
	err = tx.QueryRow("SELECT value FROM meta WHERE key='last_replay'").Scan(&lastReplay)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if now.Unix()-lastReplay >= 3600 || lastReplay > now.Unix()+3600 {
		if err := replayTx(tx, now); err != nil {
			return err
		}
	}
	var expiredPending int
	minuteCutoff := now.AddDate(0, 0, -7).Unix()
	dayCutoff := now.In(shanghai).AddDate(0, -13, 0).Unix()
	if err := tx.QueryRow("SELECT COUNT(*) FROM usage WHERE dirty=1 AND ((kind='minute' AND start<?) OR (kind='day' AND start<?))", minuteCutoff, dayCutoff).Scan(&expiredPending); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM usage WHERE (kind='minute' AND start<?) OR (kind='day' AND start<? AND dirty=0)", minuteCutoff, dayCutoff); err != nil {
		return err
	}
	var pending int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM usage WHERE dirty=1").Scan(&pending); err != nil {
		return err
	}
	compactCount := pending - queueLimit/pendingRowBytes
	if compactCount > 0 {
		// Daily cumulative totals remain authoritative, even during a long outage.
		if _, err := tx.Exec(`DELETE FROM usage WHERE (kind,start,device_id) IN
(SELECT kind,start,device_id FROM usage WHERE kind='minute' AND dirty=1 ORDER BY start LIMIT ?)`, compactCount); err != nil {
			return err
		}
	}
	if expiredPending > 0 || compactCount > 0 {
		if _, err := tx.Exec("INSERT INTO meta(key,value) VALUES('compacted','1') ON CONFLICT(key) DO UPDATE SET value='1'"); err != nil {
			return err
		}
	}
	if err := maintainHours(tx, now, queueLimit); err != nil {
		return err
	}
	return tx.Commit()
}

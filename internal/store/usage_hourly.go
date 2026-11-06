package store

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Native hours are cumulative counters produced by the Agent. The existing
// hourly table is derived from minute deltas and must never be added to these.
func EnsureHourlyUsageSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS native_hourly (
 node_id TEXT NOT NULL,device_id TEXT NOT NULL,start INTEGER NOT NULL,
 upload INTEGER NOT NULL,download INTEGER NOT NULL,uncertain INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(node_id,device_id,start));
CREATE INDEX IF NOT EXISTS native_hourly_node_start ON native_hourly(node_id,start);
CREATE TABLE IF NOT EXISTS node_hourly_coverage (
 node_id TEXT PRIMARY KEY,since INTEGER NOT NULL,through INTEGER NOT NULL,CHECK(through>=since));
CREATE TABLE IF NOT EXISTS node_hourly_gaps (
 node_id TEXT NOT NULL,start INTEGER NOT NULL,end INTEGER NOT NULL,CHECK(end>start),PRIMARY KEY(node_id,start,end));`)
	return err
}

func validateHourlyReport(r model.AgentReport, now time.Time) error {
	if r.HourlySince == nil && r.HourlyThrough == nil && len(r.Hours) == 0 {
		return nil // Older Agents remain compatible but cannot claim coverage.
	}
	if r.HourlySince == nil || r.HourlyThrough == nil || r.HourlySince.IsZero() || r.HourlyThrough.IsZero() ||
		r.HourlySince.Unix() <= 0 || r.HourlyThrough.Before(*r.HourlySince) || r.HourlyThrough.After(now.Add(time.Minute)) {
		return errors.New("小时统计覆盖时间无效")
	}
	var up, down int64
	for _, b := range r.Hours {
		if len(b.DeviceID) == 0 || len(b.DeviceID) > 128 || b.Start <= 0 || b.Start%3600 != 0 ||
			b.Start > now.Add(time.Minute).Truncate(time.Hour).Unix() || b.Upload < 0 || b.Download < 0 || b.Upload > 1<<60 || b.Download > 1<<60 {
			return errors.New("小时流量数据格式无效")
		}
		if b.Upload > math.MaxInt64-up || b.Download > math.MaxInt64-down {
			return errors.New("小时流量汇总超出范围")
		}
		up, down = up+b.Upload, down+b.Download
	}
	return nil
}

func recordHourlyReport(tx *sql.Tx, node string, r model.AgentReport, valid map[string]bool, now time.Time) error {
	if r.HourlySince == nil {
		return nil
	}
	cutoff := now.AddDate(0, -13, 0).Truncate(time.Hour).Unix()
	for _, b := range r.Hours {
		if !valid[b.DeviceID] || strings.HasPrefix(b.DeviceID, "__probe_") || b.Start < cutoff {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO native_hourly(node_id,device_id,start,upload,download,uncertain) VALUES(?,?,?,?,?,?)
ON CONFLICT(node_id,device_id,start) DO UPDATE SET upload=MAX(upload,excluded.upload),download=MAX(download,excluded.download),uncertain=MAX(uncertain,excluded.uncertain)`, node, b.DeviceID, b.Start, b.Upload, b.Download, b.Uncertain); err != nil {
			return err
		}
	}
	// Coverage is conservative across replay, backup restore and Agent retention.
	// A later since can only narrow certainty; an old delayed report cannot undo it.
	since := max(r.HourlySince.Unix(), min(cutoff, r.HourlyThrough.Unix()))
	var oldThrough int64
	err := tx.QueryRow("SELECT through FROM node_hourly_coverage WHERE node_id=?", node).Scan(&oldThrough)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && since > oldThrough {
		// Keep the precise missing interval instead of declaring all previously
		// acknowledged history unknown when an Agent narrows its coverage.
		if _, err := tx.Exec("INSERT OR IGNORE INTO node_hourly_gaps(node_id,start,end) VALUES(?,?,?)", node, oldThrough, since); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO node_hourly_coverage(node_id,since,through) VALUES(?,?,?)
ON CONFLICT(node_id) DO UPDATE SET since=MAX(since,excluded.since),through=MAX(through,excluded.through)`, node, since, r.HourlyThrough.Unix()); err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM native_hourly WHERE node_id=? AND start<?", node, cutoff)
	return err
}

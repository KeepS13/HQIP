package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

const maxLeaseQueueRows = 16384

var errLeaseCounterRegression = errors.New("lease counters cannot regress or reopen a final report")

type pendingLeaseUsage struct {
	ID       string
	Usage    model.LeaseUsage
	Version  int64
	Attempts int
}

// Successful closed records are retained as short-lived tombstones. At capacity
// only acknowledged closed records may be pruned. Unconfirmed rows are retained
// indefinitely and stop new admission rather than silently discard final usage.
func (l *ledger) leaseCapacity(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer l.db.Exec("DELETE FROM lease_billing_reports WHERE lease_id NOT IN (SELECT lease_id FROM lease_usage)")
	_, e := l.db.Exec("DELETE FROM lease_usage WHERE closed=1 AND dirty=0 AND received<?", now.Add(-24*time.Hour).Unix())
	if e != nil {
		return e
	}
	var count int
	if e = l.db.QueryRow("SELECT COUNT(*) FROM lease_usage").Scan(&count); e != nil {
		return e
	}
	if count < maxLeaseQueueRows {
		return nil
	}
	_, e = l.db.Exec("DELETE FROM lease_usage WHERE lease_id IN (SELECT lease_id FROM lease_usage WHERE closed=1 AND dirty=0 ORDER BY received LIMIT ?)", count-maxLeaseQueueRows+1)
	if e != nil {
		return e
	}
	if e = l.db.QueryRow("SELECT COUNT(*) FROM lease_usage").Scan(&count); e != nil {
		return e
	}
	if count >= maxLeaseQueueRows {
		return errors.New("durable lease queue is full; new admission paused")
	}
	return nil
}

func (l *ledger) saveLeaseUsage(id string, u model.LeaseUsage, now time.Time) (pendingLeaseUsage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := pendingLeaseUsage{ID: id, Usage: u, Version: 1}
	tx, e := l.db.Begin()
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var old model.LeaseUsage
	var version int64
	e = tx.QueryRow("SELECT upload,download,closed,applied_rate,version FROM lease_usage WHERE lease_id=?", id).Scan(&old.UploadBytes, &old.DownloadBytes, &old.Closed, &old.AppliedRateVersion, &version)
	if e == nil {
		if u.UploadBytes < old.UploadBytes || u.DownloadBytes < old.DownloadBytes || u.AppliedRateVersion < old.AppliedRateVersion || (old.Closed && !u.Closed) {
			return out, errLeaseCounterRegression
		}
		out.Version = version + 1
	} else if e == sql.ErrNoRows {
		var count int
		if e = tx.QueryRow("SELECT COUNT(*) FROM lease_usage").Scan(&count); e != nil {
			return out, e
		}
		if count >= maxLeaseQueueRows {
			return out, errors.New("durable lease queue full")
		}
	} else {
		return out, e
	}
	_, e = tx.Exec("INSERT INTO lease_usage(lease_id,upload,download,closed,applied_rate,version,dirty,received) VALUES(?,?,?,?,?,?,1,?) ON CONFLICT(lease_id) DO UPDATE SET upload=excluded.upload,download=excluded.download,closed=excluded.closed,applied_rate=excluded.applied_rate,version=excluded.version,dirty=1,received=excluded.received,attempts=0,next_attempt=0", id, u.UploadBytes, u.DownloadBytes, u.Closed, u.AppliedRateVersion, out.Version, now.Unix())
	if e != nil {
		return out, e
	}
	raw, e := json.Marshal(u.Billing)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec("INSERT INTO lease_billing_reports(lease_id,body) VALUES(?,?) ON CONFLICT(lease_id) DO UPDATE SET body=excluded.body", id, string(raw)); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

func (l *ledger) ackLeaseUsage(p pendingLeaseUsage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, e := l.db.Exec("UPDATE lease_usage SET dirty=0,attempts=0,next_attempt=0 WHERE lease_id=? AND version=?", p.ID, p.Version)
	return e
}
func (l *ledger) retryLeaseUsage(p pendingLeaseUsage, now time.Time, status int) error {
	delay := 5 * time.Second
	for i := 0; i < p.Attempts && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute || status >= 400 && status < 500 {
		delay = 5 * time.Minute
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, e := l.db.Exec("UPDATE lease_usage SET attempts=attempts+1,next_attempt=? WHERE lease_id=? AND version=?", now.Add(delay).Unix(), p.ID, p.Version)
	return e
}
func (l *ledger) pendingLeaseFinals(now time.Time) ([]pendingLeaseUsage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, e := l.db.Query("SELECT lease_id,upload,download,closed,applied_rate,version,attempts,COALESCE((SELECT body FROM lease_billing_reports b WHERE b.lease_id=lease_usage.lease_id),'null') FROM lease_usage WHERE dirty=1 AND closed=1 AND next_attempt<=? ORDER BY received LIMIT 16", now.Unix())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []pendingLeaseUsage{}
	for rows.Next() {
		var p pendingLeaseUsage
		var raw string
		if e = rows.Scan(&p.ID, &p.Usage.UploadBytes, &p.Usage.DownloadBytes, &p.Usage.Closed, &p.Usage.AppliedRateVersion, &p.Version, &p.Attempts, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &p.Usage.Billing); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (l *ledger) leaseQueueWarning() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var pending, total int
	if l.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(CASE WHEN dirty=1 AND closed=1 THEN 1 ELSE 0 END),0) FROM lease_usage").Scan(&total, &pending) != nil {
		return "连接用量队列读取失败"
	}
	if total >= maxLeaseQueueRows {
		return "连接用量队列已满，新连接暂停；需恢复控制连接或核对未确认记录"
	}
	if pending > 0 {
		return fmt.Sprintf("%d 个连接的最终用量已在本机持久保存，等待控制服务确认", pending)
	}
	return ""
}

func (a *Agent) replayLeaseFinals(parent context.Context) error {
	pending, e := a.ledger.pendingLeaseFinals(a.now())
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	for _, p := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, data, e := a.requestAdmission(ctx, "/api/v1/agents/leases/"+p.ID+"/renew", p.Usage)
		if e == nil && status == 200 && !confirmedLeaseUsage(p.ID, p.Usage, data) {
			status = 502
			e = errors.New("lease usage confirmation mismatch")
		}
		if e == nil && status == 200 {
			if e = a.ledger.ackLeaseUsage(p); e != nil {
				return e
			}
			continue
		}
		_ = a.ledger.retryLeaseUsage(p, a.now(), status)
		if e != nil || status >= 500 {
			return errors.New("final connection usage remains queued")
		}
	}
	return nil
}

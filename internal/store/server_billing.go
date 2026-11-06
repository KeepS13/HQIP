package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

const serverCycleSeconds = int64(30 * 24 * 60 * 60)

func EnsureServerBillingSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS server_billing(server_id TEXT PRIMARY KEY REFERENCES servers(id),epoch TEXT NOT NULL,started_at INTEGER NOT NULL,anchor INTEGER NOT NULL,expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS server_renewals(id TEXT PRIMARY KEY,server_id TEXT NOT NULL REFERENCES servers(id),request_id TEXT NOT NULL,payload TEXT NOT NULL,body TEXT NOT NULL,confirmed_at INTEGER NOT NULL,UNIQUE(server_id,request_id));
CREATE TABLE IF NOT EXISTS billing_daily(node_id TEXT NOT NULL REFERENCES nodes(id),start INTEGER NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,PRIMARY KEY(node_id,start));
CREATE TABLE IF NOT EXISTS billing_hourly(node_id TEXT NOT NULL REFERENCES nodes(id),start INTEGER NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,uncertain INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(node_id,start));
`)
	return err
}

// BillingQuery lets alert collection share a consistent read transaction.
type BillingQuery interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

type ServerBillingSetup struct {
	ExpiresAt *time.Time
	Actor     string
}

type ServerRenewalRequest struct {
	ExpectedRevision int64      `json:"expectedRevision"`
	RequestID        string     `json:"requestId"`
	Months           *int       `json:"months"`
	ExpiresAt        *time.Time `json:"expiresAt"`
}

type billingState struct {
	epoch                    string
	started, anchor, expires int64
}

func billingStateFor(q BillingQuery, server string) (billingState, error) {
	var b billingState
	err := q.QueryRow("SELECT epoch,started_at,anchor,expires_at FROM server_billing WHERE server_id=?", server).Scan(&b.epoch, &b.started, &b.anchor, &b.expires)
	return b, err
}

// Boundaries are a pure function of durable purchase state, so a restart or an
// offline control service cannot miss, duplicate, or move a scheduled reset.
func currentBillingCycle(b billingState, now time.Time) (start int64, index int64, next int64) {
	start = b.started
	if now.Unix() >= b.anchor && b.expires > b.anchor {
		index = (now.Unix()-b.anchor)/serverCycleSeconds + 1
		maximum := (b.expires-b.anchor-1)/serverCycleSeconds + 1
		if index > maximum {
			index = maximum
		}
		start = b.anchor + (index-1)*serverCycleSeconds
	}
	if index == 0 {
		next = b.anchor
	} else {
		next = b.anchor + index*serverCycleSeconds
	}
	if next >= b.expires {
		next = 0
	}
	return
}

func validServerExpiry(expires time.Time, now time.Time) bool {
	return !expires.IsZero() && expires.After(now) && expires.Year() <= 2200 && expires.Nanosecond() == 0 && expires.Unix()%3600 == 0
}

func setupServerBilling(tx *sql.Tx, server string, expires time.Time, revision int64, actor string, now time.Time) error {
	if !validServerExpiry(expires, now) {
		return errors.New("请选择未来的北京时间整点到期时间")
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM server_billing WHERE server_id=?", server).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	if _, err := tx.Exec("INSERT INTO server_billing(server_id,epoch,started_at,anchor,expires_at) VALUES(?,?,?,?,?)", server, NewID(), MonthStart(now), expires.Unix(), expires.Unix()); err != nil {
		return err
	}
	record := model.ServerRenewal{ID: NewID(), ServerID: server, RequestID: "initial", Kind: "initial", ExpiresAt: expires.UTC(), ConfirmedAt: now.UTC(), Actor: actor, Revision: revision}
	if err := recordServerRenewal(tx, record, "initial"); err != nil {
		return err
	}
	return cacheServerBilling(tx, server, now)
}

func recordServerRenewal(tx *sql.Tx, r model.ServerRenewal, payload string) error {
	if _, err := tx.Exec("INSERT INTO server_renewals(id,server_id,request_id,payload,body,confirmed_at) VALUES(?,?,?,?,?,?)", r.ID, r.ServerID, r.RequestID, payload, jsonString(r), r.ConfirmedAt.Unix()); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'server.renewal',?)", r.ConfirmedAt.Unix(), jsonString(map[string]string{"serverId": r.ServerID, "renewalId": r.ID, "actor": r.Actor, "kind": r.Kind}))
	return err
}

func (s *Store) RenewServer(server string, request ServerRenewalRequest, actor string) (model.Server, model.ServerRenewal, error) {
	var empty model.Server
	var record model.ServerRenewal
	if !validRequestKey(request.RequestID) || request.RequestID == "initial" || request.ExpectedRevision < 1 || (request.Months == nil) == (request.ExpiresAt == nil) {
		return empty, record, errors.New("续费请求需要有效版本、请求标识，以及月份或到期时间")
	}
	if request.Months != nil && (*request.Months < 1 || *request.Months > 1200) {
		return empty, record, errors.New("续费月份须为 1–1200 的整数")
	}
	if request.ExpiresAt != nil {
		at := request.ExpiresAt.UTC()
		request.ExpiresAt = &at
	}
	payload := jsonString(struct {
		Months    *int       `json:"months"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}{request.Months, request.ExpiresAt})
	tx, err := s.DB.Begin()
	if err != nil {
		return empty, record, err
	}
	defer tx.Rollback()
	var storedPayload, raw string
	err = tx.QueryRow("SELECT payload,body FROM server_renewals WHERE server_id=? AND request_id=?", server, request.RequestID).Scan(&storedPayload, &raw)
	if err == nil {
		if storedPayload != payload {
			return empty, record, ErrConflict
		}
		if err = json.Unmarshal([]byte(raw), &record); err != nil {
			return empty, record, err
		}
		if err = tx.Commit(); err != nil {
			return empty, record, err
		}
		v, e := s.Server(server)
		return v, record, e
	}
	if err != sql.ErrNoRows {
		return empty, record, err
	}
	var revision int64
	if err = tx.QueryRow("SELECT revision FROM servers WHERE id=? AND deleted_at=0", server).Scan(&revision); err != nil {
		return empty, record, notFound(err)
	}
	if revision != request.ExpectedRevision {
		return empty, record, ErrConflict
	}
	b, err := billingStateFor(tx, server)
	if err != nil {
		if err == sql.ErrNoRows {
			err = errors.New("请先配置服务器到期时间")
		}
		return empty, record, err
	}
	now := s.Now()
	oldExpiry := time.Unix(b.expires, 0).UTC()
	record = model.ServerRenewal{ID: NewID(), ServerID: server, RequestID: request.RequestID, PreviousExpiresAt: &oldExpiry, ConfirmedAt: now.UTC(), Actor: actor, Revision: revision + 1}
	if now.Unix() < b.expires {
		if request.Months == nil {
			return empty, record, errors.New("未到期续费请选择月数，每月按 30 天延长")
		}
		record.Kind = "advance"
		record.Months = *request.Months
		record.ExpiresAt = oldExpiry.Add(time.Duration(*request.Months) * 30 * 24 * time.Hour)
		if record.ExpiresAt.Year() > 2200 {
			return empty, record, errors.New("续费到期时间超出范围")
		}
		_, err = tx.Exec("UPDATE server_billing SET expires_at=? WHERE server_id=?", record.ExpiresAt.Unix(), server)
	} else {
		if request.ExpiresAt == nil || !validServerExpiry(*request.ExpiresAt, now) {
			return empty, record, errors.New("服务器已到期，请手动选择未来的北京时间整点到期时间")
		}
		record.Kind = "overdue"
		record.ExpiresAt = request.ExpiresAt.UTC()
		_, err = tx.Exec("UPDATE server_billing SET epoch=?,started_at=?,anchor=?,expires_at=? WHERE server_id=?", NewID(), now.Unix(), record.ExpiresAt.Unix(), record.ExpiresAt.Unix(), server)
	}
	if err != nil {
		return empty, record, err
	}
	if _, err = tx.Exec("UPDATE servers SET revision=revision+1 WHERE id=?", server); err != nil {
		return empty, record, err
	}
	if err = recordServerRenewal(tx, record, payload); err != nil {
		return empty, record, err
	}
	if err = cacheServerBilling(tx, server, now); err != nil {
		return empty, record, err
	}
	if err = tx.Commit(); err != nil {
		return empty, record, err
	}
	v, err := s.Server(server)
	return v, record, err
}

func (s *Store) ServerRenewals(server string) ([]model.ServerRenewal, error) {
	if err := s.requireServer(server); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query("SELECT body FROM server_renewals WHERE server_id=? ORDER BY confirmed_at DESC,rowid DESC", server)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ServerRenewal{}
	for rows.Next() {
		var raw string
		var r model.ServerRenewal
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func cacheServerBilling(tx *sql.Tx, server string, now time.Time) error {
	rows, err := tx.Query("SELECT id FROM nodes WHERE json_extract(config,'$.serverId')=?", server)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = RecordServerBillingUsage(tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

// Report calls this after applying authoritative daily/hourly counters in its
// own transaction. These advisory caches never feed subscription deductions.
func RecordServerBillingUsage(tx *sql.Tx, node string, now time.Time) error {
	var server string
	if err := tx.QueryRow("SELECT COALESCE(json_extract(config,'$.serverId'),'') FROM nodes WHERE id=?", node).Scan(&server); err != nil {
		return err
	}
	b, err := billingStateFor(tx, server)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	start, _, _ := currentBillingCycle(b, now)
	if _, err = tx.Exec(`INSERT INTO billing_daily(node_id,start,upload,download) SELECT node_id,start,SUM(upload),SUM(download) FROM daily WHERE node_id=? AND start>=? AND device_id NOT GLOB '__probe_*' GROUP BY node_id,start ON CONFLICT(node_id,start) DO UPDATE SET upload=MAX(upload,excluded.upload),download=MAX(download,excluded.download)`, node, DayStart(time.Unix(start, 0))); err != nil {
		return err
	}
	// All uncertain hours survive detail retention, including those after the
	// opening day. Their counters must never masquerade as this cycle's usage.
	_, err = tx.Exec(`INSERT INTO billing_hourly(node_id,start,upload,download,uncertain) SELECT node_id,start,SUM(upload),SUM(download),MAX(uncertain) FROM native_hourly WHERE node_id=? AND start>=? AND device_id NOT GLOB '__probe_*' GROUP BY node_id,start HAVING MAX(uncertain)=1 OR start<? ON CONFLICT(node_id,start) DO UPDATE SET upload=MAX(upload,excluded.upload),download=MAX(download,excluded.download),uncertain=MAX(uncertain,excluded.uncertain)`, node, start/3600*3600, DayStart(time.Unix(start, 0))+86400)
	return err
}

func addBillingUsage(out *model.ServerBilling, up, down int64) error {
	if up < 0 || down < 0 || up > math.MaxInt64-down || out.CycleUpload > math.MaxInt64-out.CycleDownload || up+down > math.MaxInt64-(out.CycleUpload+out.CycleDownload) {
		return errors.New("服务器周期流量超出可表示范围")
	}
	out.CycleUpload += up
	out.CycleDownload += down
	return nil
}

func billingDailyTotals(q BillingQuery, node string, from int64, now time.Time) (up, down int64, uncertain bool, err error) {
	var unknown int
	err = q.QueryRow(`WITH days AS (
SELECT start,MAX(upload) upload,MAX(download) download FROM (
 SELECT start,SUM(upload) upload,SUM(download) download FROM daily WHERE node_id=? AND start>=? AND start<=? AND device_id NOT GLOB '__probe_*' GROUP BY start
 UNION ALL SELECT start,upload,download FROM billing_daily WHERE node_id=? AND start>=? AND start<=?) GROUP BY start),
hours AS (SELECT start,MAX(upload) upload,MAX(download) download,MAX(uncertain) uncertain FROM (
 SELECT start,SUM(upload) upload,SUM(download) download,MAX(uncertain) uncertain FROM native_hourly WHERE node_id=? AND start>=? AND start<=? AND device_id NOT GLOB '__probe_*' GROUP BY start
 UNION ALL SELECT start,upload,download,uncertain FROM billing_hourly WHERE node_id=? AND start>=? AND start<=?) GROUP BY start),
unknown AS (SELECT ((start+28800)/86400*86400-28800) day,SUM(upload) upload,SUM(download) download FROM hours WHERE uncertain=1 GROUP BY day)
SELECT COALESCE(SUM(MAX(0,d.upload-COALESCE(u.upload,0))),0),COALESCE(SUM(MAX(0,d.download-COALESCE(u.download,0))),0),(SELECT COUNT(*) FROM hours WHERE uncertain=1)
FROM days d LEFT JOIN unknown u ON u.day=d.start
WHERE NOT EXISTS(SELECT 1 FROM node_hourly_gaps g WHERE g.node_id=? AND g.start<d.start+86400 AND g.end>d.start)`, node, from, DayStart(now), node, from, DayStart(now), node, from, now.Unix(), node, from, now.Unix(), node).Scan(&up, &down, &unknown)
	return up, down, unknown > 0, err
}

// BillingInSnapshot reads current-cycle proxy traffic, including retired nodes.
// Daily and native-hour sources never overlap: only the opening partial day
// needs hours. A mid-hour overdue renewal excludes that unresolved hour.
func BillingInSnapshot(q BillingQuery, server, node string, now time.Time) (model.ServerBilling, error) {
	out := model.ServerBilling{Status: "unconfigured", CycleComplete: true}
	b, err := billingStateFor(q, server)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	start, index, next := currentBillingCycle(b, now)
	if start%3600 != 0 {
		out.CycleComplete = false
	}
	startTime, expires := time.Unix(start, 0).UTC(), time.Unix(b.expires, 0).UTC()
	out.Status = "active"
	if now.Unix() >= b.expires {
		out.Status = "expired"
	}
	out.ExpiresAt = &expires
	out.CycleStartedAt = &startTime
	out.CycleID = fmt.Sprintf("%s:%d", b.epoch, index)
	if next > 0 {
		at := time.Unix(next, 0).UTC()
		out.NextResetAt = &at
	}
	openingDay := DayStart(startTime)
	fullDays := openingDay
	if start != openingDay {
		fullDays += 86400
	}
	filter := "json_extract(n.config,'$.serverId')=?"
	args := []any{server}
	if node != "" {
		filter += " AND n.id=?"
		args = append(args, node)
	}
	rows, err := q.Query(`SELECT n.id,n.config,n.lifecycle,n.managed,n.last_seen,n.deleted_at,COALESCE(c.since,0),COALESCE(c.through,0),COALESCE(r.stopped,0) FROM nodes n LEFT JOIN node_hourly_coverage c ON c.node_id=n.id LEFT JOIN node_retirements r ON r.node_id=n.id WHERE `+filter, args...)
	if err != nil {
		return out, err
	}
	type member struct {
		id                                     string
		created, seen, deleted, since, through int64
		managed, stopped                       bool
		lifecycle                              string
	}
	members := []member{}
	for rows.Next() {
		var m member
		var raw string
		if err = rows.Scan(&m.id, &raw, &m.lifecycle, &m.managed, &m.seen, &m.deleted, &m.since, &m.through, &m.stopped); err != nil {
			rows.Close()
			return out, err
		}
		var n model.Node
		if err = json.Unmarshal([]byte(raw), &n); err != nil {
			rows.Close()
			return out, err
		}
		m.created = n.CreatedAt.Unix()
		members = append(members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if node != "" && len(members) == 0 {
		return out, ErrNotFound
	}
	for _, m := range members {
		var up, down int64
		var dailyUnknown bool
		up, down, dailyUnknown, err = billingDailyTotals(q, m.id, fullDays, now)
		if err != nil {
			return out, err
		}
		if dailyUnknown {
			out.CycleComplete = false
		}
		var gaps int
		if err = q.QueryRow("SELECT COUNT(*) FROM node_hourly_gaps WHERE node_id=? AND start<? AND end>?", m.id, now.Unix(), start).Scan(&gaps); err != nil {
			return out, err
		}
		if gaps > 0 {
			out.CycleComplete = false
		}
		if err = addBillingUsage(&out, up, down); err != nil {
			return out, err
		}
		if m.managed && !m.stopped && m.lifecycle != "deleted" && (m.seen == 0 || m.seen < now.Add(-30*time.Second).Unix()) {
			out.CycleComplete = false
		}
		if m.managed && !m.stopped && m.lifecycle != "deleted" && (m.through == 0 || m.through < now.Add(-30*time.Second).Unix()) {
			out.CycleComplete = false
		}
		if m.managed && (m.since == 0 || m.since > max(start, m.created)) {
			out.CycleComplete = false
		}
		if start == openingDay {
			continue
		}
		if !m.managed && m.seen == 0 && up == 0 && down == 0 {
			var hadUsage int
			if err = q.QueryRow("SELECT EXISTS(SELECT 1 FROM lifetime WHERE node_id=? UNION ALL SELECT 1 FROM native_hourly WHERE node_id=? UNION ALL SELECT 1 FROM billing_hourly WHERE node_id=?)", m.id, m.id, m.id).Scan(&hadUsage); err != nil {
				return out, err
			}
			if hadUsage == 0 {
				continue
			}
		}
		hourStart := (start + 3599) / 3600 * 3600
		end := min(openingDay+86400, now.Unix())
		if m.deleted > 0 && m.deleted < end {
			end = m.deleted
		}
		if m.created >= end || end <= start {
			continue
		}
		required := max(hourStart, m.created/3600*3600)
		if start%3600 != 0 {
			out.CycleComplete = false
		}
		throughRequired := end
		if end == now.Unix() {
			throughRequired -= 30
		}
		if m.since == 0 || m.since > required || m.through < throughRequired {
			out.CycleComplete = false
		}
		var uncertain int
		err = q.QueryRow(`SELECT COALESCE(SUM(CASE WHEN uncertain=0 THEN upload ELSE 0 END),0),COALESCE(SUM(CASE WHEN uncertain=0 THEN download ELSE 0 END),0),COALESCE(MAX(uncertain),0) FROM (SELECT start,MAX(upload) upload,MAX(download) download,MAX(uncertain) uncertain FROM (SELECT start,SUM(upload) upload,SUM(download) download,MAX(uncertain) uncertain FROM native_hourly WHERE node_id=? AND start>=? AND start<? AND device_id NOT GLOB '__probe_*' GROUP BY start UNION ALL SELECT start,upload,download,uncertain FROM billing_hourly WHERE node_id=? AND start>=? AND start<?) GROUP BY start) h WHERE NOT EXISTS(SELECT 1 FROM node_hourly_gaps g WHERE g.node_id=? AND g.start<h.start+3600 AND g.end>h.start)`, m.id, hourStart, end, m.id, hourStart, end, m.id).Scan(&up, &down, &uncertain)
		if err != nil {
			return out, err
		}
		if err = addBillingUsage(&out, up, down); err != nil {
			return out, err
		}
		if uncertain != 0 {
			out.CycleComplete = false
		}
	}
	return out, nil
}

func billingActor(actor []string) string {
	if len(actor) > 0 && strings.TrimSpace(actor[0]) != "" {
		return strings.TrimSpace(actor[0])
	}
	return "administrator"
}

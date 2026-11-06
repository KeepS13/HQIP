package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Report applies monotonically increasing bucket counters in one transaction.
// Only positive daily differences contribute to lifetime totals; deleting old
// daily detail therefore never changes lifetime usage or duplicates replayed data.
func (s *Store) Report(nodeID string, r model.AgentReport) error {
	if len(r.Daily) > 4000 || len(r.Minutes) > 4000 || len(r.Hours) > 4000 || len(r.Online) > 4000 {
		return errors.New("一次上报最多包含 4000 条记录")
	}
	var existingLifecycle, existingCertificateMode string
	if err := s.DB.QueryRow("SELECT lifecycle,COALESCE(json_extract(config,'$.certificateMode'),'http') FROM nodes WHERE id=?", nodeID).Scan(&existingLifecycle, &existingCertificateMode); err != nil {
		return notFound(err)
	}
	if existingLifecycle == "active" {
		if err := s.validateCertificateReport(nodeID, r.Certificate); err != nil {
			return err
		}
	}
	if r.Certificate != nil && ((existingCertificateMode == "http" && r.Certificate.Version > 0) || (existingCertificateMode == "cloudflare" && r.Certificate.Version == 0 && r.Certificate.Error == "")) {
		r.Certificate = nil
	}
	now := s.Now()
	if err := validateHourlyReport(r, now); err != nil {
		return err
	}
	dailyCutoff := DayStart(now.AddDate(0, -13, 0))
	minuteCutoff := now.AddDate(0, 0, -7).Unix() / 60 * 60
	for _, b := range r.Daily {
		if b.Upload < 0 || b.Download < 0 || b.Upload > 1<<60 || b.Download > 1<<60 || b.Start != DayStart(time.Unix(b.Start, 0)) || b.Start > DayStart(now) {
			return errors.New("日流量数据格式无效")
		}
	}
	for _, b := range r.Minutes {
		if b.Upload < 0 || b.Download < 0 || b.Upload > 1<<60 || b.Download > 1<<60 || b.Start%60 != 0 || b.Start > now.Unix()/60*60+60 {
			return errors.New("分钟流量数据格式无效")
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var desired int64
	var lifecycle string
	if err = tx.QueryRow("SELECT desired_revision,lifecycle FROM nodes WHERE id=?", nodeID).Scan(&desired, &lifecycle); err != nil {
		return notFound(err)
	}
	if r.Revision < 0 || r.Revision > desired {
		return errors.New("配置版本无效")
	}
	// Portal lease identities remain valid for historical ledger reports after
	// credential rotation or expiry. Quota is settled only by lease reports;
	// these buckets feed node/server traffic graphs and never charge it again.
	rows, err := tx.Query("SELECT c.device_id FROM credentials c JOIN devices d ON d.id=c.device_id WHERE c.node_id=? UNION SELECT accounting_id FROM node_accounting_identities WHERE node_id=?", nodeID, nodeID)
	if err != nil {
		return err
	}
	valid := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		valid[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	online := map[string]int{}
	for id, count := range r.Online {
		if count < 0 || count > 100000 {
			return errors.New("在线实例数量无效")
		}
		if valid[id] && count > 0 {
			online[id] = count
		}
	}
	for _, b := range r.Daily {
		if !valid[b.DeviceID] || b.Start < dailyCutoff {
			continue
		}
		up, down, e := upsertBucket(tx, "daily", nodeID, b)
		if e != nil {
			return e
		}
		if up > 0 || down > 0 {
			if _, err = tx.Exec("INSERT INTO lifetime(node_id,device_id,upload,download) VALUES(?,?,?,?) ON CONFLICT(node_id,device_id) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download", nodeID, b.DeviceID, up, down); err != nil {
				return err
			}
		}
	}
	for _, b := range r.Minutes {
		if !valid[b.DeviceID] || b.Start < minuteCutoff {
			continue
		}
		up, down, e := upsertBucket(tx, "minute", nodeID, b)
		if e != nil {
			return e
		}
		if up > 0 || down > 0 {
			if _, err = tx.Exec("INSERT INTO hourly(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?) ON CONFLICT(node_id,device_id,start) DO UPDATE SET upload=upload+excluded.upload,download=download+excluded.download", nodeID, b.DeviceID, b.Start/3600*3600, up, down); err != nil {
				return err
			}
		}
	}
	if err = recordHourlyReport(tx, nodeID, r, valid, now); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE nodes SET managed=1,applied_revision=?,last_seen=?,metrics=?,online=?,core_healthy=? WHERE id=?", r.Revision, now.Unix(), jsonString(r.Metrics), jsonString(online), r.CoreHealthy, nodeID); err != nil {
		return err
	}
	if err = s.recordRemovals(tx, nodeID, r.Removals); err != nil {
		return err
	}
	if err = s.recordLifecycle(tx, nodeID, r); err != nil {
		return err
	}
	if len(r.Warnings) > 0 {
		// Agent messages can contain runtime diagnostics; persist a bounded,
		// non-secret notice rather than arbitrary remote text in audit logs.
		if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "agent-warning:"+nodeID, "节点 Agent 报告撤销处理告警，请检查节点日志"); err != nil {
			return err
		}
	}
	if r.HistoryCompacted {
		if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "history:"+nodeID, fmt.Sprint(now.Unix())); err != nil {
			return err
		}
	}
	if err = RecordServerBillingUsage(tx, nodeID, now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if r.Certificate != nil && lifecycle == "active" {
		return s.RecordCertificateStatus(nodeID, *r.Certificate)
	}
	return nil
}

func upsertBucket(tx *sql.Tx, table, nodeID string, b model.UsageBucket) (int64, int64, error) {
	var up, down int64
	err := tx.QueryRow("SELECT upload,download FROM "+table+" WHERE node_id=? AND device_id=? AND start=?", nodeID, b.DeviceID, b.Start).Scan(&up, &down)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	newUp, newDown := max(up, b.Upload), max(down, b.Download)
	if newUp == up && newDown == down && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	_, err = tx.Exec("INSERT INTO "+table+"(node_id,device_id,start,upload,download) VALUES(?,?,?,?,?) ON CONFLICT(node_id,device_id,start) DO UPDATE SET upload=MAX(upload,excluded.upload),download=MAX(download,excluded.download)", nodeID, b.DeviceID, b.Start, newUp, newDown)
	return newUp - up, newDown - down, err
}

func (s *Store) Traffic(nodeID, deviceID, period string) ([]model.TrafficPoint, error) {
	table := "minute"
	since := s.Now().Add(-24 * time.Hour).Unix()
	switch period {
	case "7d":
		table = "hourly"
		since = s.Now().AddDate(0, 0, -7).Unix()
	case "90d":
		table = "hourly"
		since = s.Now().AddDate(0, 0, -90).Unix()
	case "13m":
		table = "daily"
		since = DayStart(s.Now().AddDate(0, -13, 0))
	}
	query := "SELECT start,SUM(upload),SUM(download) FROM " + table + " WHERE start>=?"
	args := []any{since}
	if nodeID != "" {
		query += " AND node_id=?"
		args = append(args, nodeID)
	}
	if deviceID != "" {
		query += " AND device_id=?"
		args = append(args, deviceID)
	}
	query += " GROUP BY start ORDER BY start"
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.TrafficPoint{}
	for rows.Next() {
		var p model.TrafficPoint
		if err = rows.Scan(&p.At, &p.Upload, &p.Download); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

type Alert struct {
	Level    string `json:"level"`
	Message  string `json:"message"`
	NodeID   string `json:"nodeId,omitempty"`
	ServerID string `json:"serverId,omitempty"`
}

func (s *Store) Alerts(nodes []model.Node) []Alert {
	alerts := []Alert{}
	for _, n := range nodes {
		if n.MonthlyQuotaBytes != nil && *n.MonthlyQuotaBytes > 0 {
			used := n.MonthUpload + n.MonthDownload
			period := "本月"
			if n.Billing.Status != "" && n.Billing.Status != "unconfigured" {
				used = n.Billing.CycleUpload + n.Billing.CycleDownload
				period = "本周期"
			}
			ratio := float64(used) / float64(*n.MonthlyQuotaBytes)
			if ratio >= 1 {
				alerts = append(alerts, Alert{Level: "danger", Message: n.Name + " " + period + "代理流量已达到额度，连接保持正常", NodeID: n.ID})
			} else if ratio >= .8 {
				alerts = append(alerts, Alert{Level: "warning", Message: n.Name + " " + period + "代理流量已达到额度的 80%", NodeID: n.ID})
			}
		}
		if n.LastSeen != nil && s.Now().Sub(*n.LastSeen) > 30*time.Second {
			alerts = append(alerts, Alert{Level: "warning", Message: n.Name + " 心跳超过 30 秒未更新，在线情况未知", NodeID: n.ID})
		}
		if n.ServerID == "" && n.Metrics.DiskTotal > 0 && (n.Metrics.DiskUsed >= n.Metrics.DiskTotal || n.Metrics.DiskTotal-n.Metrics.DiskUsed < 512*1024*1024) {
			alerts = append(alerts, Alert{Level: "warning", Message: n.Name + " 可用磁盘不足 512 MiB", NodeID: n.ID})
		}
		if n.DesiredRevision > n.AppliedRevision {
			alerts = append(alerts, Alert{Level: "info", Message: n.Name + " 的设备配置待同步", NodeID: n.ID})
		}
		var gap string
		if s.DB.QueryRow("SELECT value FROM metadata WHERE key=?", "history:"+n.ID).Scan(&gap) == nil {
			alerts = append(alerts, Alert{Level: "warning", Message: n.Name + " 曾压缩离线上报队列，部分详细历史可能不完整", NodeID: n.ID})
		}
		var agentWarning string
		if s.DB.QueryRow("SELECT value FROM metadata WHERE key=?", "agent-warning:"+n.ID).Scan(&agentWarning) == nil {
			alerts = append(alerts, Alert{Level: "warning", Message: n.Name + "：" + agentWarning, NodeID: n.ID})
		}
	}
	return alerts
}

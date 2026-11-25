package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Report applies monotonically increasing bucket counters in one transaction.
// Only positive daily differences contribute to lifetime totals; deleting old
// daily detail therefore never changes lifetime usage or duplicates replayed data.
func (s *Store) Report(nodeID string, r model.AgentReport) error {
	return s.ReportContext(context.Background(), nodeID, r)
}

// ReportContext stops outstanding work when the submitting agent disconnects.
// A lost commit acknowledgment remains safe to replay using cumulative maxima.
func (s *Store) ReportContext(ctx context.Context, nodeID string, r model.AgentReport) error {
	if len(r.Daily) > 4000 || len(r.Minutes) > 4000 || len(r.Hours) > 4000 || len(r.Online) > 4000 {
		return errors.New("一次上报最多包含 4000 条记录")
	}
	var existingLifecycle, existingCertificateMode string
	if err := s.DB.QueryRowContext(ctx, "SELECT lifecycle,COALESCE(json_extract(config,'$.certificateMode'),'http') FROM nodes WHERE id=?", nodeID).Scan(&existingLifecycle, &existingCertificateMode); err != nil {
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
	if err := validateHostNetwork(r.HostNetwork, now); err != nil {
		return err
	}
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
	var lifecycle string
	err := retryDatabaseError(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var desired int64
		if err = tx.QueryRowContext(ctx, "SELECT desired_revision,lifecycle FROM nodes WHERE id=?", nodeID).Scan(&desired, &lifecycle); err != nil {
			return notFound(err)
		}
		if r.Revision < 0 || r.Revision > desired {
			return errors.New("配置版本无效")
		}
		// Portal lease identities remain valid for historical ledger reports after
		// credential rotation or expiry. Quota is settled only by lease reports;
		// these buckets feed node/server traffic graphs and never charge it again.
		rows, err := tx.QueryContext(ctx, "SELECT c.device_id FROM credentials c JOIN devices d ON d.id=c.device_id WHERE c.node_id=? UNION SELECT accounting_id FROM node_accounting_identities WHERE node_id=?", nodeID, nodeID)
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
		if err = recordUsageBatch(ctx, tx, "daily", nodeID, r.Daily, valid, dailyCutoff); err != nil {
			return err
		}
		if err = recordUsageBatch(ctx, tx, "minute", nodeID, r.Minutes, valid, minuteCutoff); err != nil {
			return err
		}
		if err = recordHourlyReport(ctx, tx, nodeID, r, valid, now); err != nil {
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
		if err = recordReality(tx, nodeID, r, lifecycle == "active"); err != nil {
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
		if err = recordHostNetwork(ctx, tx, nodeID, r.HostNetwork, now); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}
	if r.Certificate != nil && lifecycle == "active" {
		return s.RecordCertificateStatus(nodeID, *r.Certificate)
	}
	return nil
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

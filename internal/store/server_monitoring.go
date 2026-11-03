package store

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Persist only administrator configuration, never a stale monitoring response.
func serverConfigJSON(v model.Server) (string, error) {
	b, err := json.Marshal(map[string]any{
		"id": v.ID, "name": v.Name, "sshHost": v.SSHHost, "sshPort": v.SSHPort,
		"sshUser": v.SSHUser, "hostKey": v.HostKey, "machineId": v.MachineID,
		"platform": v.Platform, "architecture": v.Architecture, "revision": v.Revision,
		"createdAt": v.CreatedAt, "bandwidthMbps": v.BandwidthMbps,
		"monthlyQuotaBytes": v.MonthlyQuotaBytes,
	})
	return string(b), err
}

func validServerSettings(name string, bandwidth *float64, quota *int64) error {
	if strings.TrimSpace(name) == "" || len([]rune(name)) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return errors.New("服务器名称不能为空、不能超过 80 个字符且不能包含换行或空字符")
	}
	// Both comparisons deliberately reject NaN and infinities as well as zero.
	if bandwidth != nil && !(*bandwidth > 0 && *bandwidth <= 1e9) {
		return errors.New("服务器带宽必须大于 0 且不超过 1000000000 Mbps，或选择无限")
	}
	if quota != nil && !(*quota > 0 && *quota <= 1e18) {
		return errors.New("服务器月流量额度必须大于 0 且不超过 1000000000000000000 字节，或选择无限")
	}
	return nil
}

// UpdateServerSettings changes advisory capacity without SSH or node mutations.
func (s *Store) UpdateServerSettings(id, name string, bandwidth *float64, quota *int64, expected int64) (model.Server, error) {
	if err := validServerSettings(name, bandwidth, quota); err != nil {
		return model.Server{}, err
	}
	name = strings.TrimSpace(name)
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Server{}, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRow("SELECT revision FROM servers WHERE id=? AND deleted_at=0", id).Scan(&revision); err != nil {
		return model.Server{}, notFound(err)
	}
	if revision != expected {
		return model.Server{}, ErrConflict
	}
	if _, err = tx.Exec("UPDATE servers SET config=json_set(config,'$.name',?,'$.bandwidthMbps',?,'$.monthlyQuotaBytes',?,'$.revision',?),revision=revision+1 WHERE id=?", name, bandwidth, quota, revision+1, id); err != nil {
		return model.Server{}, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'server.settings',?)", s.Now().Unix(), id); err != nil {
		return model.Server{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Server{}, err
	}
	return s.Server(id)
}

// usageTotals uses independent aggregates, so daily rows cannot multiply the
// lifetime totals. Tombstoned nodes, addresses and devices remain in the ledger.
func (s *Store) usageTotals(serverID string) (model.UsageTotals, error) {
	start := MonthStart(s.Now())
	end := time.Unix(start, 0).In(Shanghai).AddDate(0, 1, 0).Unix()
	query := ""
	filter := ""
	args := []any{}
	if serverID != "" {
		query = "WITH owned AS (SELECT id FROM nodes WHERE json_extract(config,'$.serverId')=?) "
		filter = " AND node_id IN (SELECT id FROM owned)"
		args = append(args, serverID)
	}
	query += `SELECT m.upload,m.download,l.upload,l.download
FROM (SELECT COALESCE(SUM(upload),0) AS upload,COALESCE(SUM(download),0) AS download FROM daily WHERE start>=? AND start<?` + filter + `) m
CROSS JOIN (SELECT COALESCE(SUM(upload),0) AS upload,COALESCE(SUM(download),0) AS download FROM lifetime WHERE 1=1` + filter + `) l`
	args = append(args, start, end)
	var totals model.UsageTotals
	err := s.DB.QueryRow(query, args...).Scan(&totals.MonthUpload, &totals.MonthDownload, &totals.TotalUpload, &totals.TotalDownload)
	return totals, err
}

// UsageTotals remains valid after node/server deletion and is not derived from
// instantaneous rates, which also include internal connectivity probes.
func (s *Store) UsageTotals() (model.UsageTotals, error) {
	return s.usageTotals("")
}

func (s *Store) serverMonitoring(v *model.Server) error {
	totals, err := s.usageTotals(v.ID)
	if err != nil {
		return err
	}
	v.MonthUpload, v.MonthDownload = totals.MonthUpload, totals.MonthDownload
	v.TotalUpload, v.TotalDownload = totals.TotalUpload, totals.TotalDownload
	v.NodeCount, v.IPCount = 0, len(v.Addresses)
	v.ExpectedNodes, v.ReportingNodes = 0, 0
	v.RatesStatus = "none"
	v.LastSeen, v.MetricsReportedAt = nil, nil
	v.MetricsSourceNodeID, v.MetricsFresh = "", false
	v.Metrics = model.Metrics{}
	now := s.Now()
	rows, err := s.DB.Query(`SELECT n.id,n.lifecycle,n.managed,n.last_seen,n.core_healthy,n.metrics,COALESCE(r.stopped,0)
FROM nodes n LEFT JOIN node_retirements r ON r.node_id=n.id
WHERE json_extract(n.config,'$.serverId')=? ORDER BY n.last_seen DESC,n.id`, v.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, lifecycle, raw string
		var managed, healthy, stopped bool
		var seen int64
		if err = rows.Scan(&id, &lifecycle, &managed, &seen, &healthy, &raw, &stopped); err != nil {
			return err
		}
		if seen > 0 && v.LastSeen == nil {
			at := time.Unix(seen, 0).UTC()
			v.LastSeen = &at
		}
		if lifecycle != "deleted" {
			v.NodeCount++
		}
		if !managed || stopped || lifecycle == "deleted" {
			continue
		}
		v.ExpectedNodes++
		if !healthy || seen == 0 || seen < now.Add(-30*time.Second).Unix() || seen > now.Unix() {
			continue
		}
		var metrics model.Metrics
		if err = json.Unmarshal([]byte(raw), &metrics); err != nil {
			return err
		}
		// Reject impossible telemetry before summing so an otherwise valid JSON
		// number cannot overflow the aggregate or pretend to be a live report.
		if !validServerMetrics(metrics) {
			continue
		}
		v.ReportingNodes++
		// A fresh successful core sample is the existing protocol's best host
		// freshness signal. Missing memory/disk totals still mean unknown.
		if !v.MetricsFresh {
			v.Metrics.CPUPercent = metrics.CPUPercent
			v.Metrics.MemoryUsed, v.Metrics.MemoryTotal = metrics.MemoryUsed, metrics.MemoryTotal
			v.Metrics.DiskUsed, v.Metrics.DiskTotal = metrics.DiskUsed, metrics.DiskTotal
			v.MetricsFresh, v.MetricsSourceNodeID = true, id
			at := time.Unix(seen, 0).UTC()
			v.MetricsReportedAt = &at // Controller receipt time, not host sample time.
		}
		v.Metrics.UploadRate += metrics.UploadRate
		v.Metrics.DownloadRate += metrics.DownloadRate
	}
	if err = rows.Err(); err != nil {
		return err
	}
	switch {
	case v.ExpectedNodes == 0:
		v.RatesStatus = "none"
	case v.ReportingNodes == 0:
		v.RatesStatus = "unknown"
	case v.ReportingNodes < v.ExpectedNodes:
		v.RatesStatus = "partial"
	default:
		v.RatesStatus = "complete"
	}
	return nil
}

func validServerMetrics(metrics model.Metrics) bool {
	validRate := func(rate float64) bool {
		return !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate >= 0 && rate <= 1<<60
	}
	return validRate(metrics.UploadRate) && validRate(metrics.DownloadRate) && !math.IsNaN(metrics.CPUPercent) && !math.IsInf(metrics.CPUPercent, 0)
}

func (s *Store) requireServer(id string) error {
	var found string
	return notFound(s.DB.QueryRow("SELECT id FROM servers WHERE id=? AND deleted_at=0", id).Scan(&found))
}

// ServerTraffic keeps archived IP-node history in the physical server's chart.
func (s *Store) ServerTraffic(id, period string) ([]model.TrafficPoint, error) {
	if err := s.requireServer(id); err != nil {
		return nil, err
	}
	now := s.Now()
	table, since := "minute", now.Add(-24*time.Hour).Unix()
	switch period {
	case "7d":
		table, since = "hourly", now.AddDate(0, 0, -7).Unix()
	case "90d":
		table, since = "hourly", now.AddDate(0, 0, -90).Unix()
	case "13m":
		table, since = "daily", DayStart(now.AddDate(0, -13, 0))
	}
	rows, err := s.DB.Query("SELECT u.start,SUM(u.upload),SUM(u.download) FROM "+table+" u JOIN nodes n ON n.id=u.node_id WHERE json_extract(n.config,'$.serverId')=? AND u.start>=? GROUP BY u.start ORDER BY u.start", id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.TrafficPoint{}
	for rows.Next() {
		var point model.TrafficPoint
		if err = rows.Scan(&point.At, &point.Upload, &point.Download); err != nil {
			return nil, err
		}
		result = append(result, point)
	}
	return result, rows.Err()
}

// ServerNodeUsage intentionally includes deleted nodes for the usage breakdown.
func (s *Store) ServerNodeUsage(id string) ([]model.Node, error) {
	if err := s.requireServer(id); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query("SELECT id FROM nodes WHERE json_extract(config,'$.serverId')=? ORDER BY json_extract(config,'$.createdAt'),id", id)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var nodeID string
		if err = rows.Scan(&nodeID); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, nodeID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []model.Node{}
	for _, nodeID := range ids {
		n, err := s.Node(nodeID)
		if err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, nil
}

func (s *Store) ServerAlerts(servers []model.Server) []Alert {
	alerts := []Alert{}
	for _, v := range servers {
		if v.MonthlyQuotaBytes != nil && *v.MonthlyQuotaBytes > 0 {
			ratio := (float64(v.MonthUpload) + float64(v.MonthDownload)) / float64(*v.MonthlyQuotaBytes)
			if ratio >= 1 {
				alerts = append(alerts, Alert{Level: "danger", Message: v.Name + " 本月设备代理流量已达到服务器额度，连接保持正常", ServerID: v.ID})
			} else if ratio >= .8 {
				alerts = append(alerts, Alert{Level: "warning", Message: v.Name + " 本月设备代理流量已达到服务器额度的 80%", ServerID: v.ID})
			}
		}
		if v.MetricsFresh && v.Metrics.DiskTotal > 0 && (v.Metrics.DiskUsed >= v.Metrics.DiskTotal || v.Metrics.DiskTotal-v.Metrics.DiskUsed < 512*1024*1024) {
			alerts = append(alerts, Alert{Level: "warning", Message: v.Name + " 的 Agent 数据目录所在磁盘可用空间不足 512 MiB", ServerID: v.ID})
		}
	}
	return alerts
}

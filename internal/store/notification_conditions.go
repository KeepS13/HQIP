package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/KeepS13/HQIP/internal/model"
)

type notificationNodeSnapshot struct {
	node                      model.Node
	seen                      int64
	managed, healthy, stopped bool
	lifecycle                 string
	metrics                   model.Metrics
	monthUsed                 int64
}

// CollectNotificationConditions reads one consistent database snapshot. Failure
// aborts the entire observation: an unreadable table must never look recovered.
// Unknown observations preserve previous incident state without sending it.
func (s *Store) CollectNotificationConditions() ([]model.NotificationCondition, error) {
	now := s.Now()
	tx, err := s.DB.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nodes, err := notificationNodes(tx, now)
	if err != nil {
		return nil, err
	}
	out := []model.NotificationCondition{}
	month := now.In(Shanghai).Format("2006-01")
	for _, n := range nodes {
		if n.lifecycle != "active" || n.stopped {
			// Retirement is not evidence that an unhealthy service recovered.
			// Explicit tombstones cancel current/recovery notifications instead.
			for _, kind := range []string{"node.health", "certificate.issuance", "certificate.application"} {
				category := "certificate"
				if kind == "node.health" {
					category = "system"
				}
				out = append(out, notificationRemoved(kind, n.node.ID, category))
			}
			continue
		}
		name := notificationName(n.node.Name, "节点")
		path := "/nodes/" + url.PathEscape(n.node.ID)
		if n.managed {
			c := model.NotificationCondition{Key: "node.health:" + n.node.ID, Category: "system", Kind: "node.health", ResourceID: n.node.ID, Title: name + " 运行异常", DetailPath: path, Level: "danger", Stage: 1, Continuous: true, RecoverySeconds: 30}
			last := n.seen
			if last == 0 {
				last = n.node.CreatedAt.Unix()
			}
			switch {
			case last > now.Unix():
				c.Unknown = true
			case now.Unix()-last >= 60:
				c.Message = "节点心跳已中断至少 60 秒，请检查节点或管理网络。"
			case n.seen == 0 || n.seen < now.Add(-30*time.Second).Unix():
				c.Unknown = true
			case !n.healthy:
				c.Message = "节点内核持续异常，请检查节点服务。"
				c.DelaySeconds = 60
			}
			if c.Unknown || c.Message != "" {
				out = append(out, c)
			}
		} else {
			out = append(out, notificationRemoved("node.health", n.node.ID, "system"))
		}
		var billing model.ServerBilling
		if n.node.ServerID != "" {
			billing, err = BillingInSnapshot(tx, n.node.ServerID, n.node.ID, now)
			if err != nil {
				return nil, err
			}
		}
		if billing.Status != "" && billing.Status != "unconfigured" {
			if c, ok := notificationBillingQuota("node", n.node.ID, name, path, billing, n.node.MonthlyQuotaBytes); ok {
				out = append(out, c)
			}
		} else if c, ok := notificationQuota("node.month", n.node.ID, name, path, month, n.monthUsed, n.node.MonthlyQuotaBytes, []int{80, 100}); ok {
			out = append(out, c)
		}
	}
	serverConditions, err := notificationServers(tx, nodes, now, month)
	if err != nil {
		return nil, err
	}
	out = append(out, serverConditions...)
	certConditions, err := notificationCertificates(tx, now)
	if err != nil {
		return nil, err
	}
	out = append(out, certConditions...)
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func notificationNodes(tx *sql.Tx, now time.Time) ([]notificationNodeSnapshot, error) {
	start := MonthStart(now)
	end := time.Unix(start, 0).In(Shanghai).AddDate(0, 1, 0).Unix()
	rows, err := tx.Query(`SELECT n.id,n.config,n.lifecycle,n.managed,n.last_seen,n.core_healthy,n.metrics,COALESCE(r.stopped,0),COALESCE(u.used,0)
FROM nodes n LEFT JOIN node_retirements r ON r.node_id=n.id
LEFT JOIN (SELECT node_id,SUM(upload)+SUM(download) AS used FROM daily WHERE start>=? AND start<? GROUP BY node_id) u ON u.node_id=n.id
ORDER BY n.last_seen DESC,n.id`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []notificationNodeSnapshot{}
	for rows.Next() {
		var n notificationNodeSnapshot
		var id, raw, metrics string
		if err = rows.Scan(&id, &raw, &n.lifecycle, &n.managed, &n.seen, &n.healthy, &metrics, &n.stopped, &n.monthUsed); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &n.node); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(metrics), &n.metrics); err != nil {
			return nil, err
		}
		if n.monthUsed < 0 {
			return nil, errors.New("invalid notification node usage total")
		}
		n.node.ID = id
		out = append(out, n)
	}
	return out, rows.Err()
}

func notificationServers(tx *sql.Tx, nodes []notificationNodeSnapshot, now time.Time, month string) ([]model.NotificationCondition, error) {
	rows, err := tx.Query("SELECT id,config,deleted_at FROM servers ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type record struct {
		id, raw string
		deleted int64
	}
	var records []record
	for rows.Next() {
		var r record
		if err = rows.Scan(&r.id, &r.raw, &r.deleted); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	out := []model.NotificationCondition{}
	for _, record := range records {
		id, raw, deleted := record.id, record.raw, record.deleted
		var server model.Server
		if deleted != 0 {
			out = append(out, notificationRemoved("server.disk", id, "system"))
			continue
		}
		if err = json.Unmarshal([]byte(raw), &server); err != nil {
			return nil, err
		}
		name := notificationName(server.Name, "服务器")
		path := "/servers/" + url.PathEscape(id)
		var used int64
		var host *notificationNodeSnapshot
		for i := range nodes {
			n := &nodes[i]
			if n.node.ServerID != id {
				continue
			}
			// Keep tombstoned-node history and late accounting corrections.
			if n.monthUsed < 0 || n.monthUsed > int64(^uint64(0)>>1)-used {
				return nil, errors.New("invalid notification usage totals")
			}
			used += n.monthUsed
			if host == nil && n.lifecycle == "active" && n.managed && !n.stopped && n.healthy && n.seen > 0 && n.seen <= now.Unix() && n.seen >= now.Add(-30*time.Second).Unix() && validServerMetrics(n.metrics) {
				host = n
			}
		}
		billing, e := BillingInSnapshot(tx, id, "", now)
		if e != nil {
			return nil, e
		}
		if billing.Status != "unconfigured" {
			if c, ok := notificationBillingQuota("server", id, name, path, billing, server.MonthlyQuotaBytes); ok {
				out = append(out, c)
			}
		} else if c, ok := notificationQuota("server.month", id, name, path, month, used, server.MonthlyQuotaBytes, []int{80, 100}); ok {
			out = append(out, c)
		}
		if c, ok := serverExpiryCondition(id, name, billing, now); ok {
			out = append(out, c)
		}
		c := model.NotificationCondition{Key: "server.disk:" + id, Category: "system", Kind: "server.disk", ResourceID: id, Title: name + " 磁盘空间不足", DetailPath: path, Level: "warning", Stage: 1, Continuous: true}
		if host == nil || host.metrics.DiskTotal == 0 || host.metrics.DiskUsed > host.metrics.DiskTotal {
			c.Unknown = true
			out = append(out, c)
			continue
		}
		free := host.metrics.DiskTotal - host.metrics.DiskUsed
		if free < 512*1024*1024 {
			c.Message = fmt.Sprintf("Agent 数据目录所在文件系统可用空间 %s，不足 512 MiB；同一服务器合并提醒。", notificationBytes(int64(free)))
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func notificationCertificates(tx *sql.Tx, now time.Time) ([]model.NotificationCondition, error) {
	rows, err := tx.Query(`SELECT n.id,n.config,c.not_after,c.error!='',c.applied_error!='',c.issued_version FROM certificate_state c JOIN nodes n ON n.id=c.node_id WHERE n.lifecycle='active' AND n.deleted_at=0 AND NOT EXISTS(SELECT 1 FROM node_retirements r WHERE r.node_id=n.id AND r.stopped=1)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.NotificationCondition{}
	for rows.Next() {
		var id, raw string
		var after, version int64
		var failure, applyFailure bool
		var node model.Node
		if err = rows.Scan(&id, &raw, &after, &failure, &applyFailure, &version); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &node); err != nil {
			return nil, err
		}
		name := notificationName(node.Name, "节点")
		path := "/nodes/" + url.PathEscape(id)
		if after > 0 {
			if stage, days := notificationExpiry(now, time.Unix(after, 0), []int{14, 7, 3}); stage > 0 {
				message := fmt.Sprintf("节点证书将于北京时间 %s 到期（剩余不超过 %d 天）。", notificationDate(after), days)
				level := "warning"
				if days == 0 {
					message = "节点证书已于北京时间 " + notificationDate(after) + " 到期，请尽快处理。"
					level = "danger"
				}
				out = append(out, model.NotificationCondition{Key: "certificate.expiry:" + id + ":" + strconv.FormatInt(after, 10), Category: "certificate", Kind: "certificate.expiry", ResourceID: id, Title: name + " 证书到期提醒", Message: message, DetailPath: path, Level: level, Stage: stage})
			}
		}
		if failure {
			message := "节点证书签发失败，请在后台查看诊断。"
			if version > 0 {
				message = "节点证书续期失败，请在后台查看诊断；当前有效证书继续保留。"
			}
			out = append(out, model.NotificationCondition{Key: "certificate.issuance:" + id, Category: "certificate", Kind: "certificate.issuance", ResourceID: id, Title: name + " 证书处理失败", Message: message, DetailPath: path, Level: "danger", Stage: 1, Continuous: true})
		}
		if applyFailure {
			out = append(out, model.NotificationCondition{Key: "certificate.application:" + id, Category: "certificate", Kind: "certificate.application", ResourceID: id, Title: name + " 证书应用失败", Message: "节点证书应用失败，请检查节点状态；有效旧证书不会被失败版本覆盖。", DetailPath: path, Level: "danger", Stage: 1, Continuous: true})
		}
	}
	return out, rows.Err()
}

func notificationQuota(kind, id, name, path, period string, used int64, quota *int64, thresholds []int) (model.NotificationCondition, bool) {
	if quota == nil || *quota <= 0 {
		return model.NotificationCondition{}, false
	}
	stage := 0
	for _, p := range thresholds {
		// Integer ceiling avoids precision loss and multiplication overflow.
		threshold := (*quota/100)*int64(p) + ((*quota%100)*int64(p)+99)/100
		if used >= threshold {
			stage = p
		}
	}
	if stage == 0 {
		return model.NotificationCondition{}, false
	}
	level := "warning"
	if stage >= 100 {
		level = "danger"
	}
	return model.NotificationCondition{Key: kind + ":" + id + ":" + period, Category: "capacity", Kind: kind, ResourceID: id, Title: name + " 月流量提醒", Message: fmt.Sprintf("北京时间 %s 月代理流量已使用 %s / %s，达到 %d%%；仅提醒，不限速、不断网。", period, notificationBytes(used), notificationBytes(*quota), stage), DetailPath: path, Level: level, Stage: stage}, true
}

func notificationExpiry(now, expires time.Time, days []int) (int, int) {
	if !expires.After(now) {
		return len(days) + 1, 0
	}
	stage, threshold := 0, 0
	for i, d := range days {
		if !expires.After(now.Add(time.Duration(d) * 24 * time.Hour)) {
			stage, threshold = i+1, d
		}
	}
	return stage, threshold
}

func notificationName(value, fallback string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value))
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80]) + "…"
	}
	if value == "" {
		return fallback
	}
	return value
}

func notificationDate(unix int64) string {
	return time.Unix(unix, 0).In(Shanghai).Format("2006-01-02 15:04:05")
}

func notificationRemoved(kind, id, category string) model.NotificationCondition {
	return model.NotificationCondition{Key: kind + ":" + id, Kind: kind, ResourceID: id, Category: category, Continuous: true, Removed: true}
}

func notificationBytes(n int64) string {
	if n >= 1_000_000_000_000 {
		return fmt.Sprintf("%.2f TB", float64(n)/1e12)
	}
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2f MB", float64(n)/1e6)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.2f KB", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10) + " B"
}

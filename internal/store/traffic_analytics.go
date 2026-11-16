package store

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrTrafficRange = errors.New("请选择有效的北京时间日期，开始日期不得晚于结束日期，且须在最近 13 个月至今天之间")

func trafficRange(from, to string, now time.Time) (time.Time, time.Time, error) {
	today := time.Unix(DayStart(now), 0).In(Shanghai)
	if from == "" && to == "" {
		return today.AddDate(0, 0, -6), today, nil
	}
	start, err := time.ParseInLocation("2006-01-02", from, Shanghai)
	end, endErr := time.ParseInLocation("2006-01-02", to, Shanghai)
	minimum := time.Unix(DayStart(now.AddDate(0, -13, 0)), 0)
	if err != nil || endErr != nil || start.Format("2006-01-02") != from || end.Format("2006-01-02") != to || start.After(end) || start.Before(minimum) || end.After(today) {
		return time.Time{}, time.Time{}, ErrTrafficRange
	}
	return start, end, nil
}

func addTraffic(up, down *int64, u, d int64) error {
	if u < 0 || d < 0 || u > math.MaxInt64-*up || d > math.MaxInt64-*down || *up+u > math.MaxInt64-(*down+d) {
		return errors.New("流量汇总超出范围")
	}
	*up += u
	*down += d
	return nil
}

// TrafficAnalytics derives every displayed total from the same grouped daily
// query. Minute/native-hour counters and subscription quota are not added to it.
func (s *Store) TrafficAnalytics(ctx context.Context, from, to, serverID string) (model.TrafficAnalytics, error) {
	now := s.Now()
	start, end, err := trafficRange(from, to, now)
	if err != nil {
		return model.TrafficAnalytics{}, err
	}
	if serverID != "" {
		if err = s.requireServer(serverID); err != nil {
			return model.TrafficAnalytics{}, err
		}
	}
	out := model.TrafficAnalytics{
		From: start.Format("2006-01-02"), To: end.Format("2006-01-02"),
		MinDate: time.Unix(DayStart(now.AddDate(0, -13, 0)), 0).In(Shanghai).Format("2006-01-02"),
		Today:   now.In(Shanghai).Format("2006-01-02"), TimeZone: "Asia/Shanghai",
		DayCount: int(end.Sub(start)/(24*time.Hour)) + 1, ContributorKind: "server",
		Days: []model.TrafficDay{}, Contributors: []model.TrafficContribution{}, GeneratedAt: now.UTC(),
	}
	out.IncludesToday = out.To == out.Today
	if serverID != "" {
		out.ContributorKind = "node"
	}
	for at := start; !at.After(end); at = at.AddDate(0, 0, 1) {
		out.Days = append(out.Days, model.TrafficDay{Date: at.Format("2006-01-02")})
	}
	query := `SELECT d.start,d.node_id,COALESCE(json_extract(n.config,'$.serverId'),''),
COALESCE(json_extract(n.config,'$.name'),'历史节点'),COALESCE(n.lifecycle,'deleted'),
COALESCE(json_extract(v.config,'$.name'),'未归属服务器'),COALESCE(v.deleted_at,1),SUM(d.upload),SUM(d.download)
FROM daily d LEFT JOIN nodes n ON n.id=d.node_id LEFT JOIN servers v ON v.id=json_extract(n.config,'$.serverId')
WHERE d.start>=? AND d.start<? AND substr(d.device_id,1,8)!='__probe_'`
	args := []any{start.Unix(), end.AddDate(0, 0, 1).Unix()}
	if serverID != "" {
		query += " AND json_extract(n.config,'$.serverId')=?"
		args = append(args, serverID)
	}
	query += " GROUP BY d.start,d.node_id,n.config,n.lifecycle,v.config,v.deleted_at ORDER BY d.start,d.node_id"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return model.TrafficAnalytics{}, err
	}
	defer rows.Close()
	contributions := map[string]*model.TrafficContribution{}
	for rows.Next() {
		var at, deleted, up, down int64
		var nodeID, owner, nodeName, lifecycle, serverName string
		if err = rows.Scan(&at, &nodeID, &owner, &nodeName, &lifecycle, &serverName, &deleted, &up, &down); err != nil {
			return model.TrafficAnalytics{}, err
		}
		index := int((at - start.Unix()) / 86400)
		if at%86400 != start.Unix()%86400 || index < 0 || index >= len(out.Days) {
			return model.TrafficAnalytics{}, errors.New("日流量时间无效")
		}
		day := &out.Days[index]
		if err = addTraffic(&day.Upload, &day.Download, up, down); err != nil {
			return model.TrafficAnalytics{}, err
		}
		day.Total, day.HasData = day.Upload+day.Download, true
		if err = addTraffic(&out.Summary.Upload, &out.Summary.Download, up, down); err != nil {
			return model.TrafficAnalytics{}, err
		}
		id, name, removed := owner, serverName, deleted != 0
		if serverID != "" {
			id, name, removed = nodeID, nodeName, lifecycle == "deleted"
		}
		part := contributions[id]
		if part == nil {
			part = &model.TrafficContribution{ID: id, Name: name, Deleted: removed}
			contributions[id] = part
		}
		if err = addTraffic(&part.Upload, &part.Download, up, down); err != nil {
			return model.TrafficAnalytics{}, err
		}
		part.Total = part.Upload + part.Download
	}
	if err = rows.Err(); err != nil {
		return model.TrafficAnalytics{}, err
	}
	var peak int64
	for _, day := range out.Days {
		if day.HasData {
			out.Summary.RecordedDays++
			if out.Summary.PeakDate == "" || day.Total > peak {
				out.Summary.PeakDate, peak = day.Date, day.Total
			}
		}
	}
	out.Summary.Total = out.Summary.Upload + out.Summary.Download
	out.Summary.HasData = out.Summary.RecordedDays > 0
	out.Summary.DailyAverage = float64(out.Summary.Total) / float64(out.DayCount)
	for _, part := range contributions {
		out.Contributors = append(out.Contributors, *part)
	}
	sort.Slice(out.Contributors, func(i, j int) bool {
		a, b := out.Contributors[i], out.Contributors[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.ID < b.ID
	})
	return out, nil
}

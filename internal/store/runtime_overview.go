package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func monitoringStatus(expected, reporting int) string {
	switch {
	case expected == 0:
		return "none"
	case reporting == 0:
		return "unknown"
	case reporting < expected:
		return "partial"
	default:
		return "complete"
	}
}

func runtimeMetrics(raw string) (model.Metrics, bool) {
	var fields struct {
		UploadRate   *float64 `json:"uploadRate"`
		DownloadRate *float64 `json:"downloadRate"`
	}
	var metrics model.Metrics
	valid := json.Unmarshal([]byte(raw), &fields) == nil && fields.UploadRate != nil && fields.DownloadRate != nil &&
		json.Unmarshal([]byte(raw), &metrics) == nil && validServerMetrics(metrics)
	return metrics, valid
}

// RuntimeOverview counts only observed, fresh core connections. Reading this
// view never renews a session, reconciles leases, or changes subscription usage.
func (s *Store) RuntimeOverview(ctx context.Context) (model.RuntimeOverview, error) {
	now := s.Now()
	out := model.RuntimeOverview{}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.DB.QueryContext(ctx, `SELECT n.id,n.last_seen,n.core_healthy,n.metrics,n.online
FROM nodes n LEFT JOIN node_retirements r ON r.node_id=n.id
WHERE n.managed=1 AND n.lifecycle!='deleted' AND COALESCE(r.stopped,0)=0 ORDER BY n.id`)
	if err != nil {
		return out, err
	}
	type observedIdentity struct {
		node, identity string
		count          int
	}
	observed := []observedIdentity{}
	for rows.Next() {
		var id, rawMetrics, rawOnline string
		var seen int64
		var healthy bool
		if err = rows.Scan(&id, &seen, &healthy, &rawMetrics, &rawOnline); err != nil {
			rows.Close()
			return out, err
		}
		out.ExpectedNodes++
		if !healthy || seen <= 0 || seen < now.Add(-30*time.Second).Unix() || seen > now.Unix() {
			continue
		}
		at := time.Unix(seen, 0).UTC()
		if out.LastReportedAt == nil || at.After(*out.LastReportedAt) {
			out.LastReportedAt = &at
		}
		if metrics, valid := runtimeMetrics(rawMetrics); valid {
			out.ReportingNodes++
			out.UploadRate += metrics.UploadRate
			out.DownloadRate += metrics.DownloadRate
		}
		var online map[string]int
		if json.Unmarshal([]byte(rawOnline), &online) != nil || online == nil {
			continue
		}
		valid := true
		for _, count := range online {
			if count < 0 || count > 100000 {
				valid = false
			}
		}
		if !valid {
			continue
		}
		out.OnlineReportingNodes++
		for identity, count := range online {
			if count > 0 && !strings.HasPrefix(identity, "__probe_") {
				observed = append(observed, observedIdentity{id, identity, count})
				out.Instances += count
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	type identityKey struct{ node, identity string }
	known := map[identityKey]bool{}
	users, subscriptions := map[string]bool{}, map[string]bool{}
	for offset := 0; offset < len(observed); offset += usageSQLBatchSize {
		page := observed[offset:min(offset+usageSQLBatchSize, len(observed))]
		args := []any{}
		for _, entry := range page {
			args = append(args, entry.node, entry.identity)
		}
		mapped, queryErr := s.DB.QueryContext(ctx, `SELECT i.node_id,i.accounting_id,p.id,p.user_id
FROM node_accounting_identities i JOIN portal_subscriptions p ON p.id=i.subject_id
JOIN portal_users u ON u.id=p.user_id WHERE i.subject_type='subscription' AND (i.node_id,i.accounting_id) IN (`+usagePlaceholders(len(page), "(?,?)")+")", args...)
		if queryErr != nil {
			return out, queryErr
		}
		for mapped.Next() {
			var node, identity, subscription, user string
			if err = mapped.Scan(&node, &identity, &subscription, &user); err != nil {
				mapped.Close()
				return out, err
			}
			known[identityKey{node, identity}] = true
			users[user], subscriptions[subscription] = true, true
		}
		err = mapped.Err()
		mapped.Close()
		if err != nil {
			return out, err
		}
	}
	for _, entry := range observed {
		if !known[identityKey{entry.node, entry.identity}] {
			out.UnmappedInstances += entry.count
		}
	}
	out.OnlineUsers, out.ActiveSubscriptions = len(users), len(subscriptions)
	out.OnlineStatus = monitoringStatus(out.ExpectedNodes, out.OnlineReportingNodes)
	if out.UnmappedInstances > 0 && out.OnlineStatus == "complete" {
		out.OnlineStatus = "partial"
	}
	out.RatesStatus = monitoringStatus(out.ExpectedNodes, out.ReportingNodes)
	return out, nil
}

package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type audienceQuery interface {
	Query(string, ...any) (*sql.Rows, error)
}

// NodesWithAudience enriches only the administrator's node list. Background
// health checks and individual Node lookups do not pay for these aggregates.
func (s *Store) NodesWithAudience() ([]model.NodeListItem, error) {
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}
	audience, err := s.NodeAudienceCounts()
	if err != nil {
		return nil, err
	}
	out := make([]model.NodeListItem, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, model.NodeListItem{Node: node, Audience: audience[node.ID]})
	}
	return out, nil
}

// NodeAudienceCounts uses three batch queries regardless of the number of
// nodes. All membership and online reads share one database snapshot.
func (s *Store) NodeAudienceCounts() (map[string]model.NodeAudience, error) {
	return retryDatabase(func() (map[string]model.NodeAudience, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		now := s.Now()
		out, err := nodeOnlineAudience(tx, now)
		if err != nil {
			return nil, err
		}
		if err = nodeSubscribedAudience(tx, now, out); err != nil {
			return nil, err
		}
		return out, tx.Commit()
	})
}

// nodeOnlineAudience counts observed subscription users, not held leases.
func nodeOnlineAudience(q audienceQuery, now time.Time) (map[string]model.NodeAudience, error) {
	out := map[string]model.NodeAudience{}
	online := map[string]map[string]int{}
	rows, err := q.Query("SELECT id,online,last_seen,core_healthy FROM nodes WHERE lifecycle!='deleted'")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, raw string
		var last int64
		var healthy bool
		if err = rows.Scan(&id, &raw, &last, &healthy); err != nil {
			rows.Close()
			return nil, err
		}
		v := model.NodeAudience{}
		if last > 0 {
			at := time.Unix(last, 0).UTC()
			v.OnlineReportedAt = &at
		}
		if healthy && last > now.Add(-30*time.Second).Unix() && last <= now.Unix() {
			var sample map[string]int
			if json.Unmarshal([]byte(raw), &sample) == nil && sample != nil {
				users, connections := 0, 0
				v.OnlineUsers, v.OnlineConnections = &users, &connections
				online[id] = sample
			}
		}
		out[id] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	// Agent samples aggregate actual connections by accounting identity. Map
	// only subscription identities back to users; probes, legacy devices and
	// held lease reservations are not evidence of a user currently online.
	rows, err = q.Query(`SELECT a.node_id,a.accounting_id,s.user_id
	 FROM node_accounting_identities a JOIN portal_subscriptions s ON s.id=a.subject_id
	 JOIN nodes n ON n.id=a.node_id WHERE a.subject_type='subscription'
	 AND n.lifecycle!='deleted' AND n.core_healthy=1 AND n.last_seen>?`, now.Add(-30*time.Second).Unix())
	if err != nil {
		return nil, err
	}
	users := map[string]map[string]bool{}
	for rows.Next() {
		var node, accountingID, user string
		if err = rows.Scan(&node, &accountingID, &user); err != nil {
			rows.Close()
			return nil, err
		}
		count := online[node][accountingID]
		if count <= 0 {
			continue
		}
		v := out[node]
		if users[node] == nil {
			users[node] = map[string]bool{}
		}
		users[node][user] = true
		*v.OnlineUsers = len(users[node])
		*v.OnlineConnections += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func nodeSubscribedAudience(q audienceQuery, now time.Time, out map[string]model.NodeAudience) error {
	// Match userSubscription's usable-quota policy, including a still-live
	// authorized tail. Reservations alone never make an exhausted pack valid.
	rows, err := q.Query(`SELECT s.id,s.user_id,s.template_id,t.config,a.node_ids,x.upload+x.download,COALESCE(l.live_budget,0)
	 FROM portal_subscriptions s JOIN portal_users u ON u.id=s.user_id
	 JOIN subscription_allocations a ON a.subscription_id=s.id
	 JOIN portal_templates t ON t.id=s.template_id JOIN portal_usage x ON x.subscription_id=s.id
	 LEFT JOIN (SELECT subscription_id,MAX(CASE WHEN revoked=0 AND budget>upload+download THEN 1 ELSE 0 END) AS live_budget
	 FROM portal_leases WHERE closed=0 AND expires>? GROUP BY subscription_id) l ON l.subscription_id=s.id
	 WHERE s.revoked=0 AND s.expires>? AND u.enabled=1`, now.Unix(), now.Unix())
	if err != nil {
		return err
	}
	templates := map[string]model.SubscriptionTemplate{}
	subscribers := map[string]map[string]bool{}
	for rows.Next() {
		var id, user, templateID, raw, allocated string
		var used int64
		var liveBudget bool
		if err = rows.Scan(&id, &user, &templateID, &raw, &allocated, &used, &liveBudget); err != nil {
			rows.Close()
			return err
		}
		template, ok := templates[templateID]
		if !ok {
			if err = json.Unmarshal([]byte(raw), &template); err != nil {
				rows.Close()
				return err
			}
			templates[templateID] = template
		}
		if !template.Enabled || template.QuotaBytes != nil && (used >= *template.QuotaBytes || !liveBudget && *template.QuotaBytes-used < model.MinimumSubscriptionLeaseBytes) {
			continue
		}
		var ids []string
		if err = json.Unmarshal([]byte(allocated), &ids); err != nil {
			rows.Close()
			return err
		}
		seen := map[string]bool{}
		for _, node := range ids {
			v, exists := out[node]
			if !exists || seen[node] {
				continue
			}
			seen[node] = true
			if subscribers[node] == nil {
				subscribers[node] = map[string]bool{}
			}
			subscribers[node][user] = true
			v.SubscribedUsers = len(subscribers[node])
			v.ActiveSubscriptions++
			out[node] = v
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	return nil
}

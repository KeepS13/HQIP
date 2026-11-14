package store

import (
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type subscriptionNodeCandidate struct {
	id        string
	users     int
	remaining int64
	expires   int64
}

// automaticSubscriptionNode is advisory: the complete authorized node list is
// still available for manual choices. All ranking inputs share one snapshot.
func (s *Store) automaticSubscriptionNode(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	return retryDatabase(func() (string, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return "", err
		}
		defer tx.Rollback()
		candidates, err := subscriptionNodeCandidates(tx, ids, s.Now())
		if err != nil {
			return "", err
		}
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if a.users != b.users {
				return a.users < b.users
			}
			if a.remaining != b.remaining {
				return a.remaining > b.remaining
			}
			if a.expires != b.expires {
				return a.expires < b.expires
			}
			return a.id < b.id
		})
		selected := ""
		if len(candidates) > 0 {
			selected = candidates[0].id
		}
		return selected, tx.Commit()
	})
}

// subscriptionNodeCandidates reads health and whole-server capacity once per snapshot.
func subscriptionNodeCandidates(tx *sql.Tx, ids []string, now time.Time) ([]subscriptionNodeCandidate, error) {
	audience, err := nodeOnlineAudience(tx, now)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	rows, err := tx.Query(`SELECT n.id,n.config,n.capabilities,s.id,s.config
	 FROM nodes n JOIN servers s ON s.id=json_extract(n.config,'$.serverId')
	 WHERE n.lifecycle='active' AND s.deleted_at=0`)
	if err != nil {
		return nil, err
	}
	type nodeServer struct{ node, server string }
	nodes := []nodeServer{}
	servers := map[string]model.Server{}
	for rows.Next() {
		var id, raw, caps, serverID, serverRaw string
		if err = rows.Scan(&id, &raw, &caps, &serverID, &serverRaw); err != nil {
			rows.Close()
			return nil, err
		}
		if !allowed[id] || audience[id].OnlineUsers == nil || !containsCapability(caps, "admission-v1") {
			continue
		}
		var node model.Node
		if err = json.Unmarshal([]byte(raw), &node); err != nil {
			rows.Close()
			return nil, err
		}
		if node.Status != "ready" {
			continue
		}
		if _, ok := servers[serverID]; !ok {
			var server model.Server
			if err = json.Unmarshal([]byte(serverRaw), &server); err != nil {
				rows.Close()
				return nil, err
			}
			servers[serverID] = server
		}
		nodes = append(nodes, nodeServer{id, serverID})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	capacity := map[string]subscriptionNodeCandidate{}
	for id, server := range servers {
		billing, err := BillingInSnapshot(tx, id, "", now)
		if err != nil {
			return nil, err
		}
		if billing.ExpiresAt != nil && !billing.ExpiresAt.After(now) {
			continue
		}
		value := subscriptionNodeCandidate{remaining: math.MaxInt64, expires: math.MaxInt64}
		if billing.ExpiresAt != nil {
			value.expires = billing.ExpiresAt.Unix()
		}
		if server.MonthlyQuotaBytes != nil {
			up, down := billing.CycleUpload, billing.CycleDownload
			if billing.Status == "unconfigured" {
				start := MonthStart(now)
				end := time.Unix(start, 0).In(Shanghai).AddDate(0, 1, 0).Unix()
				// Include every node on the server, including retired nodes.
				err = tx.QueryRow(`SELECT COALESCE(SUM(d.upload),0),COALESCE(SUM(d.download),0)
				 FROM daily d JOIN nodes n ON n.id=d.node_id
				 WHERE json_extract(n.config,'$.serverId')=? AND d.start>=? AND d.start<?`, id, start, end).Scan(&up, &down)
				if err != nil {
					return nil, err
				}
			}
			// Subtract separately to avoid overflowing the usage sum.
			if up >= *server.MonthlyQuotaBytes || down >= *server.MonthlyQuotaBytes-up {
				continue
			}
			value.remaining = *server.MonthlyQuotaBytes - up - down
		}
		capacity[id] = value
	}
	candidates := []subscriptionNodeCandidate{}
	for _, node := range nodes {
		if value, ok := capacity[node.server]; ok {
			value.id, value.users = node.node, *audience[node.node].OnlineUsers
			candidates = append(candidates, value)
		}
	}
	return candidates, nil
}

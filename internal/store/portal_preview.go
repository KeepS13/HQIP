package store

import (
	"github.com/KeepS13/HQIP/internal/model"
)

type ClosingConnection struct {
	ID     string `json:"id"`
	UserID string `json:"userId"`
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}
type PlanImpact struct {
	AffectedUsers      int                 `json:"affectedUsers"`
	ActiveConnections  int                 `json:"activeConnections"`
	AffectedUserIDs    []string            `json:"affectedUserIds"`
	ConnectionsToClose []ClosingConnection `json:"connectionsToClose"`
}

func (s *Store) PreviewPortalPlan(id string, p model.Plan) (PlanImpact, error) {
	out := PlanImpact{AffectedUserIDs: []string{}, ConnectionsToClose: []ClosingConnection{}}
	if err := validPlan(p); err != nil {
		return out, err
	}
	rows, err := s.DB.Query("SELECT id FROM portal_users WHERE plan_id=? ORDER BY id", id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var user string
		if err = rows.Scan(&user); err != nil {
			rows.Close()
			return out, err
		}
		out.AffectedUserIDs = append(out.AffectedUserIDs, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	out.AffectedUsers = len(out.AffectedUserIDs)
	allowed := map[string]bool{}
	for _, n := range p.NodeIDs {
		allowed[n] = true
	}
	for _, user := range out.AffectedUserIDs {
		ent, err := s.Entitlement(user)
		if err != nil {
			return out, err
		}
		rows, err := s.DB.Query("SELECT id,node_id FROM portal_leases WHERE user_id=? AND closed=0 AND expires>? ORDER BY created_at,id", user, s.Now().Unix())
		if err != nil {
			return out, err
		}
		rank := 0
		for rows.Next() {
			var l ClosingConnection
			l.UserID = user
			if err = rows.Scan(&l.ID, &l.NodeID); err != nil {
				rows.Close()
				return out, err
			}
			rank++
			out.ActiveConnections++
			switch {
			case !p.Enabled:
				l.Reason = "套餐停用"
			case !allowed[l.NodeID]:
				l.Reason = "节点已移出套餐"
			case rank > p.MaxConnections:
				l.Reason = "超过新的并发连接上限"
			case p.QuotaBytes != nil && ent != nil && ent.UsedBytes >= *p.QuotaBytes:
				l.Reason = "已用流量达到新的套餐额度"
			}
			if l.Reason != "" {
				out.ConnectionsToClose = append(out.ConnectionsToClose, l)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

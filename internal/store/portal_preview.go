package store

import "github.com/KeepS13/HQIP/internal/model"

type ClosingConnection struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	SubscriptionID string `json:"subscriptionId"`
	NodeID         string `json:"nodeId"`
	Reason         string `json:"reason"`
}
type SubscriptionImpact struct {
	AllocationShortfalls  []model.AllocationShortfall `json:"allocationShortfalls"`
	AffectedUsers         int                         `json:"affectedUsers"`
	AffectedSubscriptions int                         `json:"affectedSubscriptions"`
	ActiveConnections     int                         `json:"activeConnections"`
	AffectedUserIDs       []string                    `json:"affectedUserIds"`
	ConnectionsToClose    []ClosingConnection         `json:"connectionsToClose"`
}

func (s *Store) PreviewSubscriptionTemplate(id string, p model.SubscriptionTemplate) (SubscriptionImpact, error) {
	normalizeSubscriptionTemplate(&p)
	p.ID = id
	out := SubscriptionImpact{AllocationShortfalls: []model.AllocationShortfall{}, AffectedUserIDs: []string{}, ConnectionsToClose: []ClosingConnection{}}
	if err := validSubscriptionTemplate(p); err != nil {
		return out, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	current, err := subscriptionTemplate(tx, id)
	if err != nil {
		return out, err
	}
	allocations := map[string][]string{}
	if !sameNodeSet(current.NodeIDs, p.NodeIDs) {
		holders, shortfalls, e := reconcileNodeAllocations(tx, p, s.Now())
		if e != nil {
			return out, e
		}
		out.AllocationShortfalls = shortfalls
		for _, h := range holders {
			if !h.order {
				allocations[h.id] = h.nodes
			}
		}
	}
	rows, err := tx.Query("SELECT id,user_id FROM portal_subscriptions WHERE template_id=? AND revoked=0 AND expires>? ORDER BY id", id, s.Now().Unix())
	if err != nil {
		return out, err
	}
	type member struct{ id, user string }
	members := []member{}
	users := map[string]bool{}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.id, &m.user); err != nil {
			rows.Close()
			return out, err
		}
		members = append(members, m)
		if !users[m.user] {
			users[m.user] = true
			out.AffectedUserIDs = append(out.AffectedUserIDs, m.user)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	out.AffectedUsers = len(users)
	out.AffectedSubscriptions = len(members)
	for _, m := range members {
		e, err := userSubscription(tx, m.user, m.id, s.Now())
		if err != nil {
			return out, err
		}
		nodes := e.NodeIDs
		if proposed, ok := allocations[m.id]; ok {
			nodes = proposed
		}
		allowed := map[string]bool{}
		for _, node := range nodes {
			allowed[node] = true
		}
		rows, err := tx.Query("SELECT id,node_id FROM portal_leases WHERE subscription_id=? AND closed=0 AND revoked=0 AND expires>? ORDER BY created_at,rowid", m.id, s.Now().Unix())
		if err != nil {
			return out, err
		}
		rank := 0
		for rows.Next() {
			l := ClosingConnection{UserID: m.user, SubscriptionID: m.id}
			if err = rows.Scan(&l.ID, &l.NodeID); err != nil {
				rows.Close()
				return out, err
			}
			out.ActiveConnections++
			switch {
			case !p.Enabled:
				l.Reason = "订阅已停用"
			case !allowed[l.NodeID]:
				l.Reason = "节点已移出订阅"
			case rank >= p.MaxConnections:
				l.Reason = "超过新的并发连接上限"
			case p.QuotaBytes != nil && e.UsedBytes >= *p.QuotaBytes:
				l.Reason = "已用流量达到新额度"
			case p.QuotaBytes != nil && e.UsedBytes+e.ReservedBytes > *p.QuotaBytes:
				l.Reason = "需结清已发放预算后应用新额度"
			}
			if l.Reason != "" {
				out.ConnectionsToClose = append(out.ConnectionsToClose, l)
			} else {
				rank++
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

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrSubscriptionNodes = errors.New("订阅节点选择无效")
var ErrSubscriptionNodesState = errors.New("订阅已到期或已移除，不能修改节点")

type SubscriptionNodeOption struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PublicIP   string `json:"publicIp"`
	Domain     string `json:"domain"`
	InTemplate bool   `json:"inTemplate"`
	Selected   bool   `json:"selected"`
	Selectable bool   `json:"selectable"`
	Reason     string `json:"reason"`
}

type SubscriptionNodeOptions struct {
	Subscription model.UserSubscription   `json:"subscription"`
	Nodes        []SubscriptionNodeOption `json:"nodes"`
}

// Preview and save use the same snapshot policy. Existing active, capable
// nodes may be retained through a temporary health/capacity outage.
func subscriptionNodeOptions(tx *sql.Tx, current *model.UserSubscription, now time.Time) ([]SubscriptionNodeOption, error) {
	p, err := subscriptionTemplate(tx, current.TemplateID)
	if err != nil {
		return nil, err
	}
	holders, err := allocationHolders(tx, now)
	if err != nil {
		return nil, err
	}
	selected, pool, occupied := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, id := range current.NodeIDs {
		selected[id] = true
	}
	for _, id := range p.NodeIDs {
		pool[id] = true
	}
	for _, h := range holders {
		if h.user != current.UserID || h.template != current.TemplateID || !h.order && h.id == current.ID {
			continue
		}
		reason := "已被该用户的另一份同套餐订阅占用"
		if h.order {
			reason = "已被该用户的同套餐订单预留"
		}
		for _, id := range h.nodes {
			occupied[id] = reason
		}
	}
	rows, err := tx.Query("SELECT id,config,capabilities,lifecycle,last_seen,core_healthy,online FROM nodes ORDER BY id")
	if err != nil {
		return nil, err
	}
	options, ids := []SubscriptionNodeOption{}, []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var id, raw, caps, lifecycle, online string
		var last int64
		var healthy bool
		if err = rows.Scan(&id, &raw, &caps, &lifecycle, &last, &healthy, &online); err != nil {
			rows.Close()
			return nil, err
		}
		if lifecycle == "deleted" && !selected[id] {
			continue
		}
		var node model.Node
		if err = json.Unmarshal([]byte(raw), &node); err != nil {
			rows.Close()
			return nil, err
		}
		v := SubscriptionNodeOption{ID: id, Name: node.Name, PublicIP: node.PublicIP, Domain: node.Domain, InTemplate: pool[id], Selected: selected[id]}
		var sample map[string]int
		switch {
		case lifecycle != "active":
			v.Reason = "节点正在退出或已删除"
		case !containsCapability(caps, "admission-v1"):
			v.Reason = "节点不支持订阅连接准入"
		default:
			v.Selectable = v.Selected
			switch {
			case node.Status != "ready":
				v.Reason = "节点尚未就绪"
			case !healthy || last <= now.Add(-30*time.Second).Unix() || last > now.Unix() || json.Unmarshal([]byte(online), &sample) != nil || sample == nil:
				v.Reason = "节点离线或健康上报已过期"
			}
		}
		options, ids = append(options, v), append(ids, id)
		seen[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	candidates, err := subscriptionNodeCandidates(tx, ids, now)
	if err != nil {
		return nil, err
	}
	eligible := map[string]bool{}
	for _, c := range candidates {
		eligible[c.id] = true
	}
	for i := range options {
		v := &options[i]
		if eligible[v.ID] {
			v.Selectable = true
		} else if v.Reason == "" {
			v.Reason = "服务器不可用、已到期或流量不足"
		}
		if v.Selected && v.Selectable && v.Reason != "" {
			v.Reason += "；当前已分配，可保留或移除"
		}
		if reason := occupied[v.ID]; reason != "" {
			v.Selectable, v.Reason = false, reason
		}
	}
	for _, id := range current.NodeIDs {
		if !seen[id] {
			options = append(options, SubscriptionNodeOption{ID: id, Name: id, Selected: true, InTemplate: pool[id], Reason: "节点不存在，请移除"})
		}
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Selected != options[j].Selected {
			return options[i].Selected
		}
		if options[i].Name != options[j].Name {
			return options[i].Name < options[j].Name
		}
		return options[i].ID < options[j].ID
	})
	return options, nil
}

func (s *Store) SubscriptionNodes(user, id string) (SubscriptionNodeOptions, error) {
	return retryDatabase(func() (SubscriptionNodeOptions, error) {
		var out SubscriptionNodeOptions
		tx, err := s.DB.Begin()
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		now := s.Now()
		current, err := userSubscription(tx, user, id, now)
		if err != nil {
			return out, err
		}
		out.Subscription = *current
		out.Nodes, err = subscriptionNodeOptions(tx, current, now)
		if err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}

// UpdateSubscriptionNodes replaces exactly the allocation's saved target count,
// not the template's current default. Caller-owned ids are copied. The allocation
// user lock serializes inventory changes; revision checks, node replacement and
// lease revocation commit together. Usage, periods and unsettled reservations
// remain owned by the same subscription so removed nodes can still settle usage.
// Invalid selections return ErrSubscriptionNodes; stale inventory/revisions
// return ErrConflict. Reordering an unchanged selection is an unaudited no-op.
func (s *Store) UpdateSubscriptionNodes(user, id string, ids []string, expected int64) (model.UserSubscription, error) {
	if ids == nil || len(ids) > 1000 || expected < 1 {
		return model.UserSubscription{}, ErrSubscriptionNodes
	}
	seen := map[string]bool{}
	for _, node := range ids {
		if node == "" || seen[node] {
			return model.UserSubscription{}, ErrSubscriptionNodes
		}
		seen[node] = true
	}
	// Stable storage makes reordering a no-op, without mutating the caller's slice.
	ids = append([]string{}, ids...)
	sort.Strings(ids)
	return retryDatabase(func() (model.UserSubscription, error) {
		var empty model.UserSubscription
		tx, err := s.DB.Begin()
		if err != nil {
			return empty, err
		}
		defer tx.Rollback()
		if err = lockAllocationUser(tx, user); err != nil {
			return empty, err
		}
		now := s.Now()
		current, err := userSubscription(tx, user, id, now)
		if err != nil {
			return empty, err
		}
		if current.Status == "revoked" || !current.ExpiresAt.After(now) {
			return empty, ErrSubscriptionNodesState
		}
		if current.Revision != expected {
			return empty, ErrConflict
		}
		if len(ids) != current.NodesPerSubscription {
			return empty, fmt.Errorf("%w：此订阅须选择 %d 个节点，不能增减分配数量", ErrSubscriptionNodes, current.NodesPerSubscription)
		}
		if sameNodeSet(current.NodeIDs, ids) {
			return *current, nil
		}
		options, err := subscriptionNodeOptions(tx, current, now)
		if err != nil {
			return empty, err
		}
		byID := map[string]SubscriptionNodeOption{}
		for _, option := range options {
			byID[option.ID] = option
		}
		for _, node := range ids {
			v, exists := byID[node]
			if !exists {
				// Deleted nodes are omitted from the picker, but an existing
				// tombstone means a once-valid choice became stale.
				var known int
				if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=?", node).Scan(&known); err != nil {
					return empty, err
				}
				if known > 0 {
					return empty, fmt.Errorf("%w：节点已删除，请重新加载节点列表", ErrConflict)
				}
				return empty, fmt.Errorf("%w：节点不存在", ErrSubscriptionNodes)
			}
			if !v.Selectable {
				return empty, fmt.Errorf("%w：%s（%s），请刷新节点列表", ErrConflict, v.Name, v.Reason)
			}
		}
		result, err := tx.Exec(`UPDATE portal_subscriptions SET revision=revision+1 WHERE id=? AND user_id=? AND revision=? AND revoked=0 AND expires>?`, id, user, expected, s.Now().Unix())
		if err != nil {
			return empty, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return empty, err
		}
		if count != 1 {
			return empty, ErrConflict
		}
		if _, err = tx.Exec("UPDATE subscription_allocations SET node_ids=? WHERE subscription_id=?", jsonString(ids), id); err != nil {
			return empty, err
		}
		if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user); err != nil {
			return empty, err
		}
		updated, err := userSubscription(tx, user, id, s.Now())
		if err != nil {
			return empty, err
		}
		if err = reconcileSubscriptionLeases(tx, updated, s.Now()); err != nil {
			return empty, err
		}
		audit := jsonString(map[string]any{"userId": user, "subscriptionId": id, "previousNodeIds": current.NodeIDs, "nodeIds": ids, "previousTargetCount": current.NodesPerSubscription, "targetCount": len(ids), "revision": updated.Revision})
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.nodes_updated',?)", s.Now().Unix(), audit); err != nil {
			return empty, err
		}
		if !current.ExpiresAt.After(s.Now()) {
			return empty, ErrSubscriptionNodesState
		}
		return *updated, tx.Commit()
	})
}

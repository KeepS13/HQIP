package store

import (
	"database/sql"
	"encoding/json"
	"slices"
	"sort"

	"github.com/KeepS13/HQIP/internal/model"
)

// TemplateApplyResult reports every current holder and any inventory gap. A
// shortfall is committed as a visible target, not silently reduced to capacity.
type TemplateApplyResult struct {
	AffectedSubscriptions int                         `json:"affectedSubscriptions"`
	UpdatedSubscriptions  int                         `json:"updatedSubscriptions"`
	AllocationShortfalls  []model.AllocationShortfall `json:"allocationShortfalls"`
}

// ApplySubscriptionTemplate applies the saved revision to current holders.
// Purchased periods, usage, URLs and unpaid order snapshots are never reset.
// User locks are taken in ID order before allocation/lease writes; serializable
// retry handles concurrent purchases/edits. Reapplying a complete allocation is
// a no-op; a retry can fill a previously reported gap when inventory recovers.
func (s *Store) ApplySubscriptionTemplate(id string, expected int64) (TemplateApplyResult, error) {
	return retryDatabase(func() (TemplateApplyResult, error) {
		out := TemplateApplyResult{AllocationShortfalls: []model.AllocationShortfall{}}
		tx, err := s.DB.Begin()
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		now := s.Now()
		members, err := templateApplyMembers(tx, id, now.Unix())
		if err != nil {
			return out, err
		}
		users := map[string]bool{}
		for _, h := range members {
			users[h.user] = true
		}
		ordered := make([]string, 0, len(users))
		for user := range users {
			ordered = append(ordered, user)
		}
		sort.Strings(ordered)
		for _, user := range ordered {
			if err = lockAllocationUser(tx, user); err != nil {
				return out, err
			}
		}
		p, err := subscriptionTemplate(tx, id)
		if err != nil {
			return out, err
		}
		if expected < 1 || p.Revision != expected {
			return out, ErrConflict
		}
		a, err := readAllocationSnapshot(tx, p.NodeIDs, now)
		if err != nil {
			return out, err
		}
		byID := map[string]*nodeAllocation{}
		for _, h := range a.holders {
			if !h.order {
				byID[h.id] = h
			}
		}
		changed := map[string]bool{}
		// Retain all surviving assignments before filling any gap. Pending custom
		// subscriptions get the new target but still require manual activation.
		for i, member := range members {
			h := byID[member.id]
			if h == nil {
				h = member
				a.holders = append(a.holders, h)
			}
			members[i] = h
			target := subscriptionNodeTarget(p)
			if h.manual {
				// Custom allocations remain administrator-owned, independent of
				// the automatic pool that may have been added after purchase.
				target = p.NodesPerSubscription
			}
			kept := []string{}
			for _, node := range h.nodes {
				if (h.manual || slices.Contains(p.NodeIDs, node)) && len(kept) < target {
					kept = append(kept, node)
				}
			}
			changed[h.id] = h.target != target || !sameNodeSet(h.nodes, kept)
			h.target, h.nodes = target, kept
		}
		for _, h := range members {
			current, err := userSubscription(tx, h.user, h.id, now)
			if err != nil {
				return out, err
			}
			if !current.ActivationPending {
				a.fill(h, p.NodeIDs)
			}
			out.AffectedSubscriptions++
			if len(h.nodes) < h.target {
				out.AllocationShortfalls = append(out.AllocationShortfalls, model.AllocationShortfall{UserID: h.user, SubscriptionID: h.id, Required: h.target, Assigned: len(h.nodes)})
			}
			if changed[h.id] || !sameNodeSet(current.NodeIDs, h.nodes) {
				if _, err = tx.Exec("UPDATE subscription_allocations SET target_count=?,node_ids=? WHERE subscription_id=?", h.target, jsonString(h.nodes), h.id); err != nil {
					return out, err
				}
				if _, err = tx.Exec("UPDATE portal_subscriptions SET revision=revision+1 WHERE id=?", h.id); err != nil {
					return out, err
				}
				if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", h.user); err != nil {
					return out, err
				}
				out.UpdatedSubscriptions++
				current, err = userSubscription(tx, h.user, h.id, now)
				if err != nil {
					return out, err
				}
			}
			// Live limits are read from the template. Reconcile even when the node
			// set is unchanged, preserving unsettled budgets until final reports.
			if err = reconcileSubscriptionLeases(tx, current, now); err != nil {
				return out, err
			}
		}
		// Persist acknowledgement with allocations, even for an empty audience or
		// a reported shortfall. It means this version was applied, not full stock.
		// Keep the edit revision stable so applying cannot invalidate an editor.
		p.AppliedRevision = p.Revision
		if _, err = tx.Exec("UPDATE portal_templates SET config=? WHERE id=?", jsonString(p), id); err != nil {
			return out, err
		}
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.template_applied',?)", now.Unix(), id); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}

// Include pending custom holders (no expiry yet), but never revive removed or
// expired subscriptions. Stable creation order determines scarce-node priority.
func templateApplyMembers(tx *sql.Tx, id string, now int64) ([]*nodeAllocation, error) {
	rows, err := tx.Query(`SELECT s.id,s.user_id,a.target_count,a.node_ids,CASE WHEN p.subscription_id IS NULL THEN 0 ELSE 1 END
 FROM portal_subscriptions s JOIN subscription_allocations a ON a.subscription_id=s.id
 LEFT JOIN subscription_provisioning p ON p.subscription_id=s.id
 WHERE s.template_id=? AND s.revoked=0 AND (s.expires>? OR p.activated_at=0) ORDER BY s.created_at,s.id`, id, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*nodeAllocation{}
	for rows.Next() {
		h := &nodeAllocation{template: id}
		var raw string
		if err = rows.Scan(&h.id, &h.user, &h.target, &raw, &h.manual); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &h.nodes); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

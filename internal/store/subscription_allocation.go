package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrSubscriptionStock = errors.New("该订阅对您暂无库存，请等待已有订阅到期或处理待付款订单")

func sameNodeSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if !seen[id] {
			return false
		}
	}
	return true
}

// A holder owns a stable node set. Expiry/revocation, not usable quota or the
// user's enabled flag, determines whether that set still occupies inventory.
type nodeAllocation struct {
	id, user, template string
	order              bool
	target             int
	nodes              []string
}

type allocationSnapshot struct {
	holders    []*nodeAllocation
	candidates []subscriptionNodeCandidate
}

const activeNodeReservation = `r.released=0 AND o.fulfillment_state!='fulfilled' AND
 (o.payment_state='paid' OR (o.closed_at=0 AND o.creation_state!='failed' AND o.expires_at>?))`

func allocationHolders(tx *sql.Tx, now time.Time) ([]*nodeAllocation, error) {
	rows, err := tx.Query(`SELECT s.id,s.user_id,s.template_id,a.target_count,a.node_ids
 FROM portal_subscriptions s JOIN subscription_allocations a ON a.subscription_id=s.id
 WHERE s.revoked=0 AND s.expires>? ORDER BY s.created_at,s.id`, now.Unix())
	if err != nil {
		return nil, err
	}
	out := []*nodeAllocation{}
	read := func(rows *sql.Rows, order bool) error {
		defer rows.Close()
		for rows.Next() {
			h := &nodeAllocation{order: order}
			var raw string
			if err := rows.Scan(&h.id, &h.user, &h.template, &h.target, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &h.nodes); err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	}
	if err = read(rows, false); err != nil {
		return nil, err
	}
	rows, err = tx.Query(`SELECT o.id,o.user_id,o.template_id,r.target_count,r.node_ids
 FROM payment_orders o JOIN payment_node_reservations r ON r.order_id=o.id
 WHERE `+activeNodeReservation+` ORDER BY o.created_at,o.id`, now.Unix())
	if err != nil {
		return nil, err
	}
	if err = read(rows, true); err != nil {
		return nil, err
	}
	return out, nil
}

func readAllocationSnapshot(tx *sql.Tx, ids []string, now time.Time) (*allocationSnapshot, error) {
	holders, err := allocationHolders(tx, now)
	if err != nil {
		return nil, err
	}
	candidates, err := subscriptionNodeCandidates(tx, ids, now)
	if err != nil {
		return nil, err
	}
	return &allocationSnapshot{holders: holders, candidates: candidates}, nil
}

func (a *allocationSnapshot) choices(user, template string, pool []string, self *nodeAllocation) []subscriptionNodeCandidate {
	allowed, occupied := map[string]bool{}, map[string]bool{}
	for _, id := range pool {
		allowed[id] = true
	}
	users := map[string]map[string]bool{}
	for _, h := range a.holders {
		for _, id := range h.nodes {
			if users[id] == nil {
				users[id] = map[string]bool{}
			}
			users[id][h.user] = true
			if h.user == user && h.template == template {
				occupied[id] = true
			}
		}
	}
	// self may be new and not yet in holders.
	if self != nil {
		for _, id := range self.nodes {
			occupied[id] = true
		}
	}
	out := []subscriptionNodeCandidate{}
	for _, c := range a.candidates {
		if allowed[c.id] && !occupied[c.id] {
			c.users = len(users[c.id])
			out = append(out, c)
		}
	}
	return out
}

func (a *allocationSnapshot) fill(h *nodeAllocation, pool []string) {
	choices := a.choices(h.user, h.template, pool, h)
	// Shuffle before stable sorting: randomize only the fully equal tier and
	// persist the result, never reshuffle an existing assignment on reads.
	rand.Shuffle(len(choices), func(i, j int) { choices[i], choices[j] = choices[j], choices[i] })
	sort.SliceStable(choices, func(i, j int) bool {
		x, y := choices[i], choices[j]
		if x.users != y.users {
			return x.users < y.users
		}
		if x.remaining != y.remaining {
			return x.remaining > y.remaining
		}
		return x.expires < y.expires
	})
	for _, c := range choices {
		if len(h.nodes) >= h.target {
			break
		}
		h.nodes = append(h.nodes, c.id)
	}
}

func lockAllocationUser(tx *sql.Tx, user string) error {
	// A database row write serializes independent processes. PostgreSQL retries
	// a conflicting serializable snapshot; SQLite obtains its writer lock here.
	_, err := tx.Exec("UPDATE portal_users SET revision=revision WHERE id=?", user)
	return err
}

func expireNodeReservations(tx *sql.Tx, now time.Time) error {
	_, err := tx.Exec(`UPDATE payment_node_reservations SET released=1 WHERE released=0 AND order_id IN
 (SELECT id FROM payment_orders WHERE payment_state='unpaid' AND (closed_at>0 OR creation_state='failed' OR expires_at<=?))`, now.Unix())
	return err
}

func reserveOrderNodes(tx *sql.Tx, order, user string, p model.SubscriptionTemplate, target int, now time.Time) ([]string, error) {
	if err := expireNodeReservations(tx, now); err != nil {
		return nil, err
	}
	a, err := readAllocationSnapshot(tx, p.NodeIDs, now)
	if err != nil {
		return nil, err
	}
	h := &nodeAllocation{id: order, user: user, template: p.ID, order: true, target: target, nodes: []string{}}
	for _, old := range a.holders {
		if old.order && old.id == order {
			h = old
			break
		}
	}
	a.fill(h, p.NodeIDs)
	if len(h.nodes) < h.target {
		return nil, ErrSubscriptionStock
	}
	_, err = tx.Exec(`INSERT INTO payment_node_reservations(order_id,target_count,node_ids,released) VALUES(?,?,?,0)
 ON CONFLICT(order_id) DO UPDATE SET node_ids=excluded.node_ids,released=0`, order, h.target, jsonString(h.nodes))
	return h.nodes, err
}

func allocateSubscriptionNodes(tx *sql.Tx, user string, p model.SubscriptionTemplate, now time.Time) ([]string, error) {
	a, err := readAllocationSnapshot(tx, p.NodeIDs, now)
	if err != nil {
		return nil, err
	}
	h := &nodeAllocation{user: user, template: p.ID, target: p.NodesPerSubscription, nodes: []string{}}
	a.fill(h, p.NodeIDs)
	if len(h.nodes) < h.target {
		return nil, ErrSubscriptionStock
	}
	return h.nodes, nil
}

// Reconcile a proposed pool without writing, so preview and save share exactly
// the same policy. Retain every surviving grant/reservation before filling any
// gaps; otherwise an older subscription could steal a later one's node.
func reconcileNodeAllocations(tx *sql.Tx, p model.SubscriptionTemplate, now time.Time) ([]*nodeAllocation, []model.AllocationShortfall, error) {
	a, err := readAllocationSnapshot(tx, p.NodeIDs, now)
	if err != nil {
		return nil, nil, err
	}
	allowed := map[string]bool{}
	for _, id := range p.NodeIDs {
		allowed[id] = true
	}
	changed := []*nodeAllocation{}
	for _, h := range a.holders {
		if h.template != p.ID {
			continue
		}
		kept := []string{}
		for _, id := range h.nodes {
			if allowed[id] {
				kept = append(kept, id)
			}
		}
		h.nodes = kept
		changed = append(changed, h)
	}
	shortfalls := []model.AllocationShortfall{}
	for _, h := range changed {
		a.fill(h, p.NodeIDs)
		if len(h.nodes) < h.target {
			v := model.AllocationShortfall{UserID: h.user, Required: h.target, Assigned: len(h.nodes)}
			if h.order {
				v.OrderID = h.id
			} else {
				v.SubscriptionID = h.id
			}
			shortfalls = append(shortfalls, v)
		}
	}
	return changed, shortfalls, nil
}

func persistNodeAllocations(tx *sql.Tx, holders []*nodeAllocation) error {
	for _, h := range holders {
		query := "UPDATE subscription_allocations SET node_ids=? WHERE subscription_id=?"
		if h.order {
			query = "UPDATE payment_node_reservations SET node_ids=? WHERE order_id=?"
		}
		if _, err := tx.Exec(query, jsonString(h.nodes), h.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SubscriptionCatalog(user string) ([]model.CatalogSubscription, error) {
	return retryDatabase(func() ([]model.CatalogSubscription, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		rows, err := tx.Query("SELECT config FROM portal_templates ORDER BY rowid DESC")
		if err != nil {
			return nil, err
		}
		items := []model.CatalogSubscription{}
		ids := []string{}
		for rows.Next() {
			var raw string
			var p model.SubscriptionTemplate
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			if err = json.Unmarshal([]byte(raw), &p); err != nil {
				rows.Close()
				return nil, err
			}
			normalizeSubscriptionTemplate(&p)
			if p.Enabled && p.Listed {
				items = append(items, model.CatalogSubscription{SubscriptionTemplate: p})
				ids = append(ids, p.NodeIDs...)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		a, err := readAllocationSnapshot(tx, ids, s.Now())
		if err != nil {
			return nil, err
		}
		for i := range items {
			p := &items[i]
			p.AvailableCopies = len(a.choices(user, p.ID, p.NodeIDs, nil)) / p.NodesPerSubscription
			if p.AvailableCopies == 0 {
				p.AvailabilityReason = "暂无足够的可分配节点，请等待已有订阅到期或节点恢复"
				for _, h := range a.holders {
					if h.order && h.user == user && h.template == p.ID {
						p.AvailabilityReason = "节点已为待处理订单预留，请先处理已有订单"
						break
					}
				}
			}
		}
		return items, tx.Commit()
	})
}

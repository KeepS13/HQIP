package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// ErrRenewalNodes is a recoverable allocation conflict. Before payment the
// caller can show the reason; after receipt the paid order can be retried.
var ErrRenewalNodes = errors.New("续费节点需要处理")

// renewalReplacementNodes plans against the caller's locked allocation snapshot.
// A nil result means every original node can stay. No choices are reserved or
// persisted here: receipt must inspect the latest assignment and server expiry
// even when an earlier checkout succeeded.
func renewalReplacementNodes(tx *sql.Tx, user, subscription string, plan model.SubscriptionTemplate, now time.Time) ([]string, error) {
	var raw string
	var manual bool
	err := tx.QueryRow(`SELECT a.node_ids,CASE WHEN p.subscription_id IS NULL THEN 0 ELSE 1 END
		FROM subscription_allocations a LEFT JOIN subscription_provisioning p ON p.subscription_id=a.subscription_id
		WHERE a.subscription_id=?`, subscription).Scan(&raw, &manual)
	if err != nil {
		return nil, err
	}
	var original []string
	if err = json.Unmarshal([]byte(raw), &original); err != nil {
		return nil, err
	}
	if len(original) == 0 {
		return nil, fmt.Errorf("%w：此订阅尚无已分配节点，请联系管理员分配节点", ErrRenewalNodes)
	}
	kept, err := retainedRenewalNodes(tx, original, now)
	if err != nil || len(kept) == len(original) {
		return nil, err
	}
	if manual {
		return nil, fmt.Errorf("%w：定制订阅的原节点已到期或已移除，请联系管理员替换", ErrRenewalNodes)
	}
	inventory, err := readAllocationSnapshot(tx, plan.NodeIDs, now)
	if err != nil {
		return nil, err
	}
	// Replace lost assignments one for one, including on historical instances
	// with a zero target. A template edit must never resize a paid renewal.
	h := &nodeAllocation{user: user, template: plan.ID, target: len(original), nodes: kept}
	inventory.fill(h, plan.NodeIDs)
	if len(h.nodes) < h.target {
		return nil, fmt.Errorf("%w：原节点已到期或已移除，套餐内可替换节点不足，请联系管理员补齐", ErrRenewalNodes)
	}
	return h.nodes, nil
}

// Existing assignments only need a live inventory record and an unexpired
// server at the instant of renewal. Health, quota, pool membership and the
// subscription's future end date are irrelevant. Keep the original order.
func retainedRenewalNodes(tx *sql.Tx, original []string, now time.Time) ([]string, error) {
	args := make([]any, 0, 1+len(original))
	args = append(args, now.Unix())
	for _, id := range original {
		args = append(args, id)
	}
	rows, err := tx.Query(`SELECT n.id FROM nodes n JOIN servers s ON s.id=json_extract(n.config,'$.serverId')
		LEFT JOIN server_billing b ON b.server_id=s.id
		WHERE n.lifecycle='active' AND s.deleted_at=0 AND (b.expires_at IS NULL OR b.expires_at>?)
		AND n.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(original)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	present := make(map[string]bool, len(original))
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		present[id] = true
	}
	kept := make([]string, 0, len(original))
	for _, id := range original {
		if present[id] {
			kept = append(kept, id)
		}
	}
	return kept, rows.Err()
}

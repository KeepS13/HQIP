package store

import "github.com/KeepS13/HQIP/internal/model"

// subscriptionNodeTarget snapshots the actual count when granting or applying a
// template. A ceiling larger than a nonempty selected pool means all selected
// nodes. Never cap against healthy/free inventory: outages or occupied nodes
// must still report stock shortages. Empty pools retain manual activation counts.
// Keep the configured ceiling intact so later explicit application can expand.
func subscriptionNodeTarget(p model.SubscriptionTemplate) int {
	if len(p.NodeIDs) == 0 {
		return p.NodesPerSubscription
	}
	return min(p.NodesPerSubscription, len(p.NodeIDs))
}

package store

import (
	"encoding/json"

	"github.com/KeepS13/HQIP/internal/model"
)

// SubscriptionCatalog exposes the configured ceiling and computes purchasable
// copies from the effective selected-node target, using one inventory snapshot.
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
			p.ManualProvisioning = len(p.NodeIDs) == 0
			if p.ManualProvisioning {
				p.AvailabilityReason = "付款后提供订阅链接，管理员分配节点并开通后开始计时"
				continue
			}
			p.AvailableCopies = len(a.choices(user, p.ID, p.NodeIDs, nil)) / subscriptionNodeTarget(p.SubscriptionTemplate)
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

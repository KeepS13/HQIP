package store

import (
	"context"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrNodeUserTrafficPage = errors.New("分页参数无效：offset 须在 0–1000000 之间，limit 须在 1–100 之间")

// This reads the authoritative raw lifetime ledger only. Lease reservations,
// billed portal_usage, hourly/daily buckets and host NIC totals must not be
// added to it. Identity mappings survive subscription expiry/revocation, so
// historical users remain visible; unowned legacy/probe identities stay out.
// The node prefix uses both existing composite primary keys for lookup.
const nodeUserTrafficSQL = `WITH usage AS (
 SELECT u.id AS user_id,u.email,u.enabled,
 COALESCE(SUM(l.upload),0) AS upload,COALESCE(SUM(l.download),0) AS download,
 MAX(CASE WHEN l.node_id IS NOT NULL THEN 1 ELSE 0 END) AS has_data
 FROM node_accounting_identities a
 JOIN portal_subscriptions s ON s.id=a.subject_id
 JOIN portal_users u ON u.id=s.user_id
 LEFT JOIN lifetime l ON l.node_id=a.node_id AND l.device_id=a.accounting_id
 WHERE a.node_id=? AND a.subject_type='subscription'
 GROUP BY u.id,u.email,u.enabled
), page AS (
 SELECT * FROM usage ORDER BY upload+download DESC,user_id ASC LIMIT ? OFFSET ?
)
SELECT n.id,c.total_users,COALESCE(p.user_id,''),COALESCE(p.email,''),
 COALESCE(p.enabled,0),COALESCE(p.upload,0),COALESCE(p.download,0),COALESCE(p.has_data,0)
FROM nodes n CROSS JOIN (SELECT COUNT(*) AS total_users FROM usage) c
LEFT JOIN page p ON 1=1 WHERE n.id=?
ORDER BY p.upload+p.download DESC,p.user_id ASC`

// NodeUserTraffic returns an owned, paginated snapshot for a single node.
// Count, node existence and rows share one SQL statement/snapshot, including
// empty or out-of-range pages; no write transaction or application lock is
// acquired. A missing node returns ErrNotFound, invalid paging returns
// ErrNodeUserTrafficPage, and cancellation/query/overflow errors propagate.
func (s *Store) NodeUserTraffic(ctx context.Context, nodeID string, offset, limit int) (model.NodeUserTrafficPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return model.NodeUserTrafficPage{}, ErrNodeUserTrafficPage
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.DB.QueryContext(ctx, nodeUserTrafficSQL, nodeID, limit, offset, nodeID)
	if err != nil {
		return model.NodeUserTrafficPage{}, err
	}
	defer rows.Close()
	out := model.NodeUserTrafficPage{Items: []model.NodeUserTraffic{}, Offset: offset, Limit: limit, GeneratedAt: s.Now().UTC()}
	for rows.Next() {
		var item model.NodeUserTraffic
		if err = rows.Scan(&out.NodeID, &out.TotalUsers, &item.UserID, &item.Email, &item.Enabled, &item.Upload, &item.Download, &item.HasData); err != nil {
			return model.NodeUserTrafficPage{}, err
		}
		if item.UserID == "" { // LEFT JOIN placeholder for an existing node with an empty page.
			continue
		}
		var up, down int64
		if err = addTraffic(&up, &down, item.Upload, item.Download); err != nil {
			return model.NodeUserTrafficPage{}, err
		}
		item.Total = up + down
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return model.NodeUserTrafficPage{}, err
	}
	if out.NodeID == "" {
		return model.NodeUserTrafficPage{}, ErrNotFound
	}
	return out, nil
}

package store

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
)

type NodeSubscriptionReference struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DefaultNode bool   `json:"defaultNode"`
	Enabled     bool   `json:"enabled"`
	Listed      bool   `json:"listed"`
}

type NodeSubscriptionConflict struct {
	Templates []NodeSubscriptionReference
}

func (e *NodeSubscriptionConflict) Error() string {
	names := make([]string, 0, len(e.Templates))
	for _, template := range e.Templates {
		names = append(names, template.Name)
	}
	return "节点仍被订阅模板引用（" + strings.Join(names, "、") + "），请先在订阅管理中移除该节点，再删除节点"
}

func (e *NodeSubscriptionConflict) Unwrap() error { return ErrConflict }

type nodeReferenceQuery interface {
	Query(string, ...any) (*sql.Rows, error)
}

// Hidden and disabled templates retain their membership references. Legacy
// defaults are ignored. DeleteNode uses this query within its transaction.
func nodeSubscriptionReferences(q nodeReferenceQuery, node string) ([]NodeSubscriptionReference, error) {
	rows, err := q.Query(`SELECT id,config FROM portal_templates
WHERE EXISTS(SELECT 1 FROM json_each(portal_templates.config,'$.nodeIds') WHERE value=?)
ORDER BY id`, node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []NodeSubscriptionReference{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var template model.SubscriptionTemplate
		if err := json.Unmarshal([]byte(raw), &template); err != nil {
			return nil, err
		}
		refs = append(refs, NodeSubscriptionReference{ID: id, Name: template.Name,
			Enabled: template.Enabled, Listed: template.Listed})
	}
	return refs, rows.Err()
}

func (s *Store) NodeSubscriptionReferences(node string) ([]NodeSubscriptionReference, error) {
	return nodeSubscriptionReferences(s.DB, node)
}

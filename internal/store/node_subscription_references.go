package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

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
	Templates         []NodeSubscriptionReference
	UserSubscriptions []NodeUserSubscriptionReference
}

type NodeUserSubscriptionReference struct {
	ID           string `json:"id"`
	UserID       string `json:"userId"`
	Email        string `json:"email"`
	TemplateID   string `json:"templateId"`
	TemplateName string `json:"templateName"`
}

func (e *NodeSubscriptionConflict) Error() string {
	if len(e.UserSubscriptions) > 0 {
		return "节点仍被用户订阅引用，请先在用户详情中修改节点，解除用户订阅及套餐模板的引用后再删除"
	}
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

// Disabled users/templates and exhausted grants retain their allocations.
// Expired/revoked subscriptions only retain history and do not block deletion.
func nodeUserSubscriptionReferences(q nodeReferenceQuery, node string, now time.Time) ([]NodeUserSubscriptionReference, error) {
	rows, err := q.Query(`SELECT s.id,s.user_id,u.email,s.template_id,t.config,a.node_ids
 FROM portal_subscriptions s JOIN subscription_allocations a ON a.subscription_id=s.id
 JOIN portal_users u ON u.id=s.user_id JOIN portal_templates t ON t.id=s.template_id
	 WHERE s.revoked=0 AND s.expires>?
	 ORDER BY s.user_id,s.id`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []NodeUserSubscriptionReference{}
	for rows.Next() {
		var ref NodeUserSubscriptionReference
		var raw, allocated string
		var template model.SubscriptionTemplate
		if err = rows.Scan(&ref.ID, &ref.UserID, &ref.Email, &ref.TemplateID, &raw, &allocated); err != nil {
			return nil, err
		}
		var ids []string
		if err = json.Unmarshal([]byte(allocated), &ids); err != nil {
			return nil, err
		}
		found := false
		for _, id := range ids {
			if id == node {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if err = json.Unmarshal([]byte(raw), &template); err != nil {
			return nil, err
		}
		ref.TemplateName = template.Name
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *Store) NodeUserSubscriptionReferences(node string) ([]NodeUserSubscriptionReference, error) {
	return nodeUserSubscriptionReferences(s.DB, node, s.Now())
}

package store

import (
	"errors"

	"github.com/KeepS13/HQIP/internal/model"
)

// ClientNodeCatalog is built only from subscriptions already resolved for the
// authenticated user. It never creates credentials or changes their selection.
func (s *Store) ClientNodeCatalog(subscriptions []model.UserSubscription) ([]model.ClientNodeEndpoint, error) {
	nodes := []model.ClientNodeEndpoint{}
	seen := map[string]bool{}
	for _, subscription := range subscriptions {
		if subscription.Status != "active" || !subscription.ExpiresAt.After(s.Now()) {
			continue
		}
		for _, id := range subscription.NodeIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			node, err := s.Node(id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if node.Lifecycle != "active" {
				continue
			}
			nodes = append(nodes, model.ClientNodeEndpoint{ID: node.ID, Name: node.Name, Server: node.PublicIP, PublicIP: node.PublicIP, Port: node.Port, SNI: node.Domain, Ready: node.Status == "ready" && s.nodeAdmissionCapable(id)})
		}
	}
	return nodes, nil
}

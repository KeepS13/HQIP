package client

import "time"

type NodeChoiceView struct {
	SubscriptionID string `json:"subscriptionId"`
	NodeID         string `json:"nodeId"`
	Name           string `json:"name"`
	Ready          bool   `json:"ready"`
}

func activeSubscription(subscription Subscription) bool {
	return subscription.Status == "active" && subscription.ExpiresAt.After(time.Now())
}

func nodeChoices(profile Profile) ([]NodeChoiceView, []Node) {
	catalog := profile.AvailableNodes
	if catalog == nil {
		catalog = profile.Nodes // Compatibility with controllers before catalog support.
	}
	byID := make(map[string]Node, len(catalog))
	for _, node := range catalog {
		node.Password = ""
		byID[node.ID] = node
	}
	choices, nodes := []NodeChoiceView{}, []Node{}
	seen := map[string]bool{}
	for _, subscription := range profile.Subscriptions {
		if !activeSubscription(subscription) {
			continue
		}
		ids := subscription.NodeIDs
		if ids == nil && subscription.ID == profile.SubscriptionID {
			for _, node := range profile.Nodes {
				ids = append(ids, node.ID)
			}
		}
		// Subscription IDs authorize physical nodes. Protocol aliases are
		// selectable only when the server's credential-free catalog advertises
		// the matching endpoint; never infer a second authorization or allocation.
		expanded := append([]string{}, ids...)
		for _, id := range ids {
			if endpoint, ok := byID[id+":vless"]; ok && endpoint.NodeID == id && endpoint.Protocol == "vless" {
				expanded = append(expanded, endpoint.ID)
			}
		}
		ids = expanded
		seenInSubscription := map[string]bool{}
		for _, id := range ids {
			node, ok := byID[id]
			if !ok || seenInSubscription[id] {
				continue
			}
			seenInSubscription[id] = true
			choices = append(choices, NodeChoiceView{SubscriptionID: subscription.ID, NodeID: id, Name: node.Name, Ready: node.Ready})
			if !seen[id] {
				nodes = append(nodes, node)
				seen[id] = true
			}
		}
	}
	return choices, nodes
}

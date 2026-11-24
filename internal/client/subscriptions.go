package client

import (
	"context"
	"errors"
	"time"
)

func usableSubscription(p Profile) bool {
	return p.SubscriptionID != "" && p.Subscription != nil && p.Subscription.ID == p.SubscriptionID && p.Subscription.Status == "active" && p.Subscription.ExpiresAt.After(time.Now())
}

func (c *Controller) preferenceKeyLocked(p Profile) string {
	return c.saved.ServerURL + "\n" + p.DeviceID
}

func (c *Controller) rememberLocked() {
	// Idle drafts already own the account preference. A routine profile refresh
	// describes the previous authorization and must not rewrite that choice.
	if c.localChoiceLocked() != nil {
		return
	}
	p := c.saved.Profile
	if p.DeviceID == "" || p.SubscriptionID == "" {
		return
	}
	key := c.preferenceKeyLocked(p)
	v := c.saved.Preferences[key]
	// Replace the nested map so a failed persistence transaction can restore
	// its previous preference without retaining mutations through an alias.
	nodes := make(map[string]string, len(v.NodeIDs)+1)
	for id, node := range v.NodeIDs {
		nodes[id] = node
	}
	v.NodeIDs = nodes
	v.SubscriptionID = p.SubscriptionID
	v.NodeIDs[p.SubscriptionID] = c.saved.SelectedNodeID
	c.saved.Preferences[key] = v
}

func (c *Controller) rememberedNodeLocked(p Profile) string {
	id := c.saved.Preferences[c.preferenceKeyLocked(p)].NodeIDs[p.SubscriptionID]
	for _, node := range p.Nodes {
		if node.ID == id {
			return id
		}
	}
	return automaticNode(p)
}

func automaticNode(p Profile) string {
	for _, node := range p.Nodes {
		if node.ID == p.DefaultNodeID && node.Ready {
			return node.ID
		}
	}
	return ""
}

// Restore only an explicit choice previously made by this account on this PC.
// A newly assigned package never becomes a silent fallback for an expired one.
func (c *Controller) restoreRemembered(api *API, p Profile) {
	c.mu.Lock()
	if c.api != api || !c.restoreSelection || c.view.SelectionPending {
		c.mu.Unlock()
		return
	}
	c.restoreSelection = false
	id := c.saved.Preferences[c.preferenceKeyLocked(p)].SubscriptionID
	allowed := false
	for _, v := range p.Subscriptions {
		if v.ID == id && v.Status == "active" && v.ExpiresAt.After(time.Now()) {
			allowed = true
		}
	}
	c.mu.Unlock()
	if p.SubscriptionID == "" && allowed {
		_ = c.SelectSubscription(id)
	}
}

// SelectSubscription saves the preference, switching asynchronously if a
// connection is already requested. Idle selection never acquires credentials.
func (c *Controller) SelectSubscription(id string) error {
	return c.selectSubscriptionNode(id, "")
}

// SelectSubscriptionNode validates cached membership and saves the next target.
// An active connection follows the latest choice; idle selection stays local.
func (c *Controller) SelectSubscriptionNode(id, nodeID string) error {
	if nodeID == "" {
		return errors.New("请选择节点")
	}
	return c.selectSubscriptionNode(id, nodeID)
}

func (c *Controller) selectSubscriptionNode(id, nodeID string) error {
	return c.stageLocalChoice(id, nodeID)
}

// Caller holds routeMu and mu. The closure runs after mu is released; only an
// active connection or explicit Connect reaches this authorization transaction.
func (c *Controller) beginSubscriptionChangeLocked(id, nodeID string) func() {
	c.selectionUncertain = c.api
	c.selectionGeneration++
	g := c.selectionGeneration
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	if c.cancelSelection != nil {
		c.cancelSelection()
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 150*time.Second)
	c.cancelSelection = cancel
	c.view.SelectionPending = true
	c.view.DesiredSubscriptionID = id
	c.view.DesiredNodeID = nodeID
	c.view.LastError = ""
	c.restoreSelection = false
	if c.view.ConnectionIntent {
		c.view.RunState = "connecting"
	}
	api := c.api
	return func() { c.changeSubscription(ctx, cancel, api, id, nodeID, g) }
}

package client

import (
	"context"
	"errors"
	"strings"
)

// localChoice is a persisted user preference, never an authorization. Profile
// and Selected* retain the server-authorized identity until connection work.
// AccountKey prevents a new login from inheriting another account's choice.
type localChoice struct {
	AccountKey     string `json:"accountKey"`
	SubscriptionID string `json:"subscriptionId"`
	NodeID         string `json:"nodeId"`
}

func (c *Controller) localChoiceLocked() *localChoice {
	choice := c.saved.LocalChoice
	if c.api == nil || choice == nil || choice.AccountKey != c.preferenceKeyLocked(c.saved.Profile) {
		return nil
	}
	return choice
}

// Only the exported snapshot projects a draft choice. Connection workers keep
// reading the authorized profile; neither a refresh nor an old reply can turn a
// local choice into a connection or overwrite it. Caller holds mu.
func (c *Controller) presentLocalChoiceLocked(v *View) {
	choice := c.localChoiceLocked()
	if choice == nil {
		return
	}
	v.DesiredSubscriptionID, v.DesiredNodeID = choice.SubscriptionID, choice.NodeID
	v.Subscription = nil
	v.Nodes, v.DefaultNodeID = []NodeView{}, ""
	for _, node := range v.NodeChoices {
		if node.SubscriptionID == choice.SubscriptionID {
			v.Nodes = append(v.Nodes, NodeView{node.NodeID, node.Name, node.Ready})
		}
	}
	for _, sub := range c.view.Subscriptions {
		if sub.ID == choice.SubscriptionID {
			copy := sub
			v.Subscription = &copy
			v.DefaultNodeID = sub.DefaultNodeID
			break
		}
	}
}

// Validate cached catalog membership, not credentials. Connecting revalidates
// against the server profile; choosing while offline never grants permission.
func (c *Controller) validateChoiceLocked(id, nodeID string) (string, error) {
	if c.api == nil || c.closed {
		return "", errors.New("请先登录用户账户")
	}
	var subscription *Subscription
	for _, sub := range c.view.Subscriptions {
		if sub.ID == id {
			copy := sub
			subscription = &copy
			break
		}
	}
	if subscription == nil || !activeSubscription(*subscription) || (subscription.QuotaBytes != nil && subscription.UsedBytes >= *subscription.QuotaBytes) {
		return "", errors.New("该订阅不可用，请选择有效的订阅")
	}
	choices, _ := nodeChoices(c.saved.Profile)
	ready := func(node string) bool {
		for _, choice := range choices {
			if choice.SubscriptionID == id && choice.NodeID == node && choice.Ready {
				return true
			}
		}
		return false
	}
	if nodeID != "" {
		if !ready(nodeID) {
			return "", errors.New("该节点不属于有效订阅，或暂时不可用")
		}
		return nodeID, nil
	}
	remembered := c.saved.Preferences[c.preferenceKeyLocked(c.saved.Profile)].NodeIDs[id]
	if ready(remembered) {
		return remembered, nil
	}
	if ready(subscription.DefaultNodeID) {
		return subscription.DefaultNodeID, nil
	}
	return "", nil // A package without a ready default remains selected but off.
}

// Save and publish the choice without waiting for route/network work. When the
// user already wants a connection, retire the old generation and queue a switch.
// A failed save must not cancel the working transport or its authorization.
func (c *Controller) stageLocalChoice(id, nodeID string) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.mu.Lock()
	beforeSub, beforeNode := c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID
	if choice := c.localChoiceLocked(); choice != nil {
		beforeSub, beforeNode = choice.SubscriptionID, choice.NodeID
	}
	err := c.stageLocalChoiceLocked(id, nodeID)
	choice := c.localChoiceLocked()
	if err != nil || !c.view.ConnectionIntent || choice == nil ||
		(choice.SubscriptionID == beforeSub && choice.NodeID == beforeNode) {
		c.mu.Unlock()
		return err
	}
	c.generation++
	c.selectionGeneration++
	c.cancelRecoveryLocked()
	c.cancelDelayLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	if c.cancelSelection != nil {
		c.cancelSelection()
	}
	c.view.SelectionPending = true
	c.view.RunState, c.view.LastError = "connecting", ""
	c.view.DesiredSubscriptionID, c.view.DesiredNodeID = choice.SubscriptionID, choice.NodeID
	api, generation := c.api, c.generation
	c.mu.Unlock()
	c.launch(func() { c.switchActiveChoice(api, generation) })
	return nil
}

// Caller holds mu. Persistence is independent of whether the choice will be
// used now (connection intent on) or saved for the next explicit Connect.
func (c *Controller) stageLocalChoiceLocked(id, nodeID string) error {
	nodeID, err := c.validateChoiceLocked(id, nodeID)
	if err != nil {
		return err
	}
	key := c.preferenceKeyLocked(c.saved.Profile)
	choice := &localChoice{key, id, nodeID}
	if old := c.localChoiceLocked(); old != nil && *old == *choice {
		return nil
	}
	oldChoice, oldPreference := c.saved.LocalChoice, c.saved.Preferences[key]
	pref := SelectionPreference{SubscriptionID: id, NodeIDs: map[string]string{}}
	for sub, node := range oldPreference.NodeIDs {
		pref.NodeIDs[sub] = node
	}
	pref.NodeIDs[id] = nodeID
	c.saved.LocalChoice, c.saved.Preferences[key] = choice, pref
	if err = c.persistLocked(); err != nil {
		c.saved.LocalChoice, c.saved.Preferences[key] = oldChoice, oldPreference
		return err
	}
	c.restoreSelection = false
	// Choosing cannot repair a rules-integrity or account error. Clear only
	// obsolete selection diagnostics; Connect/Sync own other recovery messages.
	if c.view.LastError == "暂无可用节点，请稍后刷新或手动选择节点" || strings.HasPrefix(c.view.LastError, "订阅切换") {
		c.view.LastError = ""
	}
	if c.view.RunState == "failed" && c.view.LastError == "" {
		c.view.RunState = "disconnected"
	}
	return nil
}

// connectLocalChoiceLocked either consumes an already-authorized local node or
// returns the worker that acquires the target subscription. Caller owns routeMu
// and mu, and must unlock mu before launch. Failures retain the explicit draft.
func (c *Controller) connectLocalChoiceLocked() (func(), error) {
	choice := c.localChoiceLocked()
	if choice == nil {
		if c.selectionUncertain != c.api || c.api == nil {
			return nil, nil
		}
		choice = &localChoice{c.preferenceKeyLocked(c.saved.Profile), c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID}
	}
	node, err := c.validateChoiceLocked(choice.SubscriptionID, choice.NodeID)
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, errors.New("暂无可用节点，请先选择节点")
	}
	authorized := false
	for _, candidate := range c.saved.Profile.Nodes {
		if candidate.ID == node && candidate.Ready {
			authorized = true
			break
		}
	}
	if c.selectionUncertain == c.api || choice.SubscriptionID != c.saved.Profile.SubscriptionID || !authorized || !usableSubscription(c.saved.Profile) || len(c.bundle.Rules) == 0 {
		c.view.ConnectionIntent = true
		return c.beginSubscriptionChangeLocked(choice.SubscriptionID, node), nil
	}
	oldSub, oldNode := c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID
	oldOffline, oldUntil := c.saved.Offline, c.view.OfflineUntil
	c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID = choice.SubscriptionID, node
	c.saved.LocalChoice = nil
	if c.saved.Offline != nil && !c.offlineValidLocked() {
		c.saved.Offline, c.view.OfflineUntil = nil, nil
	}
	if err = c.persistLocked(); err != nil {
		c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID, c.saved.LocalChoice = oldSub, oldNode, choice
		c.saved.Offline, c.view.OfflineUntil = oldOffline, oldUntil
		return nil, err
	}
	c.view.DesiredSubscriptionID, c.view.DesiredNodeID = choice.SubscriptionID, node
	return nil, nil
}

package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

func usableSubscription(p Profile) bool {
	return p.SubscriptionID != "" && p.Subscription != nil && p.Subscription.ID == p.SubscriptionID && p.Subscription.Status == "active" && p.Subscription.ExpiresAt.After(time.Now())
}

func (c *Controller) preferenceKeyLocked(p Profile) string {
	return c.saved.ServerURL + "\n" + p.DeviceID
}

func (c *Controller) rememberLocked() {
	p := c.saved.Profile
	if p.DeviceID == "" || p.SubscriptionID == "" {
		return
	}
	key := c.preferenceKeyLocked(p)
	v := c.saved.Preferences[key]
	if v.NodeIDs == nil {
		v.NodeIDs = map[string]string{}
	}
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

// SelectSubscription changes only this native session. The worker serializes
// all core changes, while cancellation and local disconnect remain immediate.
func (c *Controller) SelectSubscription(id string) error {
	return c.selectSubscriptionNode(id, "")
}

// SelectSubscriptionNode publishes one subscription/node intent. The target
// node is applied only after the old subscription drains and the new profile
// has been validated, so the default node is never connected in between.
func (c *Controller) SelectSubscriptionNode(id, nodeID string) error {
	if nodeID == "" {
		return errors.New("请选择节点")
	}
	return c.selectSubscriptionNode(id, nodeID)
}

func (c *Controller) selectSubscriptionNode(id, nodeID string) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.api == nil || c.closed {
		c.mu.Unlock()
		return errors.New("请先登录用户账户")
	}
	allowed := false
	for _, v := range c.view.Subscriptions {
		if v.ID == id && v.Status == "active" && v.ExpiresAt.After(time.Now()) {
			allowed = true
		}
	}
	if !allowed {
		c.mu.Unlock()
		return errors.New("该订阅不可用，请选择有效的订阅")
	}
	if nodeID != "" {
		choices, _ := nodeChoices(c.saved.Profile)
		found := false
		for _, choice := range choices {
			if choice.SubscriptionID == id && choice.NodeID == nodeID && choice.Ready {
				found = true
			}
		}
		if !found {
			c.mu.Unlock()
			return errors.New("该节点不属于有效订阅，或暂时不可用")
		}
	}
	if !c.view.SelectionPending && c.saved.SelectedSubscriptionID == id && c.view.DesiredSubscriptionID == id && c.view.RunState != "failed" && (nodeID == "" || c.saved.SelectedNodeID == nodeID) {
		c.mu.Unlock()
		return nil
	}
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
	if nodeID != "" {
		c.view.DesiredNodeID = nodeID
	}
	c.view.LastError = ""
	c.restoreSelection = false
	if c.view.ConnectionIntent {
		c.view.RunState = "connecting"
	}
	api := c.api
	c.mu.Unlock()
	if !c.launch(func() { c.changeSubscription(ctx, cancel, api, id, nodeID, g) }) {
		cancel()
	}
	return nil
}

func (c *Controller) changeSubscription(ctx context.Context, cancel context.CancelFunc, api *API, id, nodeID string, selection uint64) {
	defer cancel()
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	current := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.closed && c.api == api && c.selectionGeneration == selection && ctx.Err() == nil
	}
	if !current() {
		c.mu.Lock()
		timedOut := c.api == api && c.selectionGeneration == selection && !c.closed && ctx.Err() != nil
		if timedOut {
			c.view.SelectionPending = false
			c.cancelSelection = nil
			c.view.ConnectionIntent = false
			c.view.RunState = "failed"
			c.view.ActualNodeID, c.view.ActualSubscriptionID = "", ""
			c.view.LastError = "订阅切换超时，VPN 已关闭，请重新选择"
		}
		c.mu.Unlock()
		if timedOut {
			_ = c.engine.Stop()
		}
		return
	}
	err := c.engine.Stop()
	c.mu.Lock()
	if c.api == api && c.selectionGeneration == selection {
		c.view.ActualNodeID = ""
		c.view.ActualSubscriptionID = ""
	}
	c.mu.Unlock()
	var p Profile
	if err == nil && current() {
		// A canceled previous request may already have committed its choice.
		// Refresh before the revision-bound drain, then select after closure.
		for attempt := 0; attempt < 3; attempt++ {
			var fresh Profile
			fresh, _, _, err = api.Profile(ctx, "")
			if err != nil || !current() {
				break
			}
			if !fresh.Enabled {
				err = ErrUnauthorized
				break
			}
			err = api.Drain(ctx, fresh.SubscriptionID, fresh.SelectionRevision)
			if errors.Is(err, ErrConflict) {
				continue
			}
			if err != nil || !current() {
				break
			}
			err = api.WaitDrained(ctx, fresh.SubscriptionID, fresh.SelectionRevision)
			if errors.Is(err, ErrConflict) {
				continue
			}
			if err != nil || !current() {
				break
			}
			p, err = api.SelectSubscription(ctx, id, fresh.SelectionRevision)
			if !errors.Is(err, ErrConflict) {
				break
			}
		}
	}
	var bundle rules.Bundle
	var data []byte
	if err == nil && current() {
		if !p.Enabled {
			err = ErrUnauthorized
		} else if !usableSubscription(p) || p.SubscriptionID != id {
			err = errors.New("订阅已失效，VPN 已关闭，请重新选择")
		} else {
			if nodeID != "" {
				found := false
				for _, node := range p.Nodes {
					if node.ID == nodeID && node.Ready {
						found = true
					}
				}
				if !found {
					err = errors.New("所选节点已不可用，VPN 已关闭，请重新选择")
				}
			}
			if err == nil {
				data, err = api.Rules(ctx, p.Rules.URL)
			}
			if err == nil {
				digest := sha256.Sum256(data)
				if hex.EncodeToString(digest[:]) != p.Rules.Hash {
					err = errors.New("规则校验失败，VPN 已关闭")
				}
			}
			if err == nil {
				err = json.Unmarshal(data, &bundle)
			}
			if err == nil {
				err = rules.Validate(bundle)
			}
		}
	}
	if errors.Is(err, ErrUnauthorized) {
		_ = c.expireSession(api)
		return
	}
	c.routeMu.Lock()
	c.mu.Lock()
	if c.api != api || c.selectionGeneration != selection || c.closed {
		c.mu.Unlock()
		c.routeMu.Unlock()
		return
	}
	if ctx.Err() != nil && err == nil {
		err = ctx.Err()
	}
	if err == nil {
		tmp := filepath.Join(c.dir, "rules.tmp")
		if err = os.WriteFile(tmp, data, 0600); err == nil {
			err = os.Rename(tmp, filepath.Join(c.dir, "rules.json"))
		}
	}
	c.view.SelectionPending = false
	c.cancelSelection = nil
	if err != nil {
		c.view.ConnectionIntent = false
		c.view.RunState = "failed"
		c.view.LastError = "订阅切换失败：" + err.Error()
		c.etag = ""
		c.mu.Unlock()
		c.routeMu.Unlock()
		return
	}
	c.saved.SelectedNodeID = c.rememberedNodeLocked(p)
	if nodeID != "" {
		c.saved.SelectedNodeID = nodeID
	}
	c.saved.SelectedSubscriptionID = id
	c.saved.Profile = p
	c.bundle = bundle
	c.etag = ""
	c.populateLocked(p)
	c.view.DesiredSubscriptionID = id
	c.view.DesiredNodeID = c.saved.SelectedNodeID
	c.rememberLocked()
	now := time.Now()
	c.view.LastSync = &now
	c.view.SyncError = ""
	c.view.LastError = ""
	c.view.ProcessedConfigRevision = p.Revision
	if c.saved.SelectedNodeID == "" {
		c.view.LastError = "暂无可用节点，请稍后刷新或手动选择节点"
	}
	on := c.view.ConnectionIntent
	if !on {
		c.view.RunState = "cached"
		c.view.AppliedConfigRevision = p.Revision
	}
	err = c.persistLocked()
	if err != nil {
		c.view.ConnectionIntent = false
		c.view.RunState = "failed"
		c.view.LastError = "无法保存订阅选择"
		on = false
	}
	c.mu.Unlock()
	c.routeMu.Unlock()
	if on {
		c.launch(c.applyLatest)
	} else {
		c.launch(c.report)
	}
}

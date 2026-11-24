package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/KeepS13/HQIP/internal/rules"
	"os"
	"path/filepath"
	"time"
)

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
	c.mu.Lock()
	cacheHash, cachedBundle := c.saved.Profile.Rules.Hash, c.bundle
	c.mu.Unlock()
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
			if err == nil && p.Rules.Hash == cacheHash && len(cachedBundle.Rules) > 0 {
				// The cache was hash-checked when installed. An unchanged rules
				// reference is shared by subscriptions; switching is not a refresh.
				bundle = cachedBundle
			} else if err == nil {
				data, err = api.Rules(ctx, p.Rules.URL)
			}
			if err == nil && data != nil {
				digest := sha256.Sum256(data)
				if hex.EncodeToString(digest[:]) != p.Rules.Hash {
					err = errors.New("规则校验失败，VPN 已关闭")
				}
			}
			if err == nil && data != nil {
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
	if err == nil && data != nil {
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
	// An idle choice made after Disconnect must survive an older response.
	// Active choices invalidate this worker's selection generation above.
	if choice := c.localChoiceLocked(); choice != nil && choice.SubscriptionID == id && choice.NodeID == nodeID {
		c.saved.LocalChoice = nil
	}
	c.saved.SelectedSubscriptionID = id
	c.saved.Profile = p
	c.selectionUncertain = nil
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

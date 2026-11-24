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

func (c *Controller) Refresh() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Sync(ctx)
}
func (c *Controller) Sync(ctx context.Context) (result error) {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	ctx, cancel := c.workContext(ctx)
	defer cancel()
	c.mu.Lock()
	requestedAPI, completed := c.api, c.syncCompleted
	c.mu.Unlock()
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.mu.Lock()
	// Concurrent explicit/event/login requests share the completed result; they
	// must not become a queue of identical imports after a slow account response.
	if c.api == requestedAPI && c.syncCompleted != completed {
		err := c.syncError
		c.mu.Unlock()
		return err
	}
	api := c.api
	etag := c.etag
	selection := c.selectionGeneration
	generation := c.generation
	pending := c.view.SelectionPending
	c.mu.Unlock()
	if api == nil || pending {
		return nil
	}
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.api == api {
			c.syncCompleted++
			c.syncError = result
			if result == nil {
				c.syncedAPI = api
			}
		}
	}()
	p, newTag, unchanged, e := api.Profile(ctx, etag)
	if e != nil {
		if errors.Is(e, ErrUnauthorized) {
			_ = c.expireSession(api)
			return e
		}
		if errors.Is(e, context.Canceled) {
			return e
		}
		c.mu.Lock()
		current := c.api == api && c.selectionGeneration == selection && c.generation == generation
		if current {
			if retryable(e) {
				c.view.ControlUnavailable = true
			}
			if retryable(e) && c.recoveryReady {
				c.view.ControlUnavailable = true
				if !c.offlineValidLocked() {
					c.startRecoveryLocked("account_unavailable")
				}
			} else {
				c.view.SyncError = e.Error()
			}
		}
		c.mu.Unlock()
		if current && !retryable(e) && !errors.Is(e, context.Canceled) {
			c.failGeneration(generation, e.Error())
		}
		return e
	}
	if unchanged {
		c.mu.Lock()
		if c.api != api || selection != c.selectionGeneration || generation != c.generation {
			c.mu.Unlock()
			return nil
		}
		now := c.clockNow()
		c.view.LastSync = &now
		c.view.ControlUnavailable = false
		c.rememberOfflineLocked(false)
		c.view.SyncError = ""
		_ = c.persistLocked()
		c.mu.Unlock()
		c.report()
		return nil
	}
	c.mu.Lock()
	currentSelection := c.api == api && c.selectionGeneration == selection && generation == c.generation && !c.view.SelectionPending
	c.mu.Unlock()
	if !currentSelection {
		return nil
	}
	if !p.Enabled {
		_ = c.expireSession(api)
		return ErrUnauthorized
	}
	if subscriptionFailure(p, c.clockNow()) != nil {
		err := c.cacheUnavailable(api, p, generation)
		c.restoreRemembered(api, p)
		return err
	}
	c.mu.Lock()
	old := c.saved.Profile
	b := c.bundle
	selected := c.saved.SelectedNodeID
	c.mu.Unlock()
	if p.SelectionRevision < old.SelectionRevision || (p.SubscriptionID == old.SubscriptionID && p.Revision < old.Revision) {
		return nil
	}
	configurationChanged := connectionConfigurationChanged(old, p, selected)
	if old.Rules.Hash != p.Rules.Hash || len(b.Rules) == 0 {
		data, e := api.Rules(ctx, p.Rules.URL)
		if e != nil {
			if errors.Is(e, ErrUnauthorized) {
				_ = c.expireSession(api)
				return e
			}
			if retryable(e) {
				c.mu.Lock()
				if c.api == api && c.generation == generation {
					c.view.ControlUnavailable = true
					if !c.offlineValidLocked() {
						c.startRecoveryLocked("rules_unavailable")
					}
				}
				c.mu.Unlock()
				return e
			}
			return c.rejectProfile(api, p, generation, e)
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != p.Rules.Hash {
			return c.rejectProfile(api, p, generation, errors.New("规则校验失败，已停止代理；未覆盖本地规则缓存"))
		}
		if e = json.Unmarshal(data, &b); e != nil {
			return c.rejectProfile(api, p, generation, errors.New("规则格式错误"))
		}
		if e = rules.Validate(b); e != nil {
			return c.rejectProfile(api, p, generation, e)
		}
		// Serialize cache publication with logout/login so an older in-flight
		// profile response cannot restore rules after the active session changed.
		c.routeMu.Lock()
		c.mu.Lock()
		current := c.api == api && c.selectionGeneration == selection && generation == c.generation && !c.view.SelectionPending
		c.mu.Unlock()
		if !current {
			c.routeMu.Unlock()
			return nil
		}
		tmp := filepath.Join(c.dir, "rules.tmp")
		if e = os.WriteFile(tmp, data, 0600); e == nil {
			e = os.Rename(tmp, filepath.Join(c.dir, "rules.json"))
		}
		c.routeMu.Unlock()
		if e != nil {
			return c.rejectProfile(api, p, generation, e)
		}
	}
	c.mu.Lock()
	if c.api != api || c.selectionGeneration != selection || generation != c.generation || c.view.SelectionPending {
		c.mu.Unlock()
		return nil
	}
	if p.SelectionRevision < c.saved.Profile.SelectionRevision || (p.SubscriptionID == c.saved.Profile.SubscriptionID && p.Revision < c.saved.Profile.Revision) {
		c.mu.Unlock()
		return nil
	}
	if c.recoveryReady {
		authorized := p.SubscriptionID == c.saved.SelectedSubscriptionID
		if !authorized {
			c.mu.Unlock()
			c.failGeneration(generation, "当前订阅授权已移除，VPN 已关闭")
			return nil
		}
	}
	if p.SubscriptionID != c.saved.SelectedSubscriptionID {
		c.saved.SelectedNodeID = c.rememberedNodeLocked(p)
	}
	allowed := false
	for _, n := range p.Nodes {
		if n.ID == c.saved.SelectedNodeID {
			allowed = true
		}
	}
	if !allowed && c.recoveryReady && c.saved.Offline != nil {
		c.mu.Unlock()
		c.failGeneration(generation, "原节点授权已移除，VPN 已关闭，请手动选择其他节点")
		return nil
	}
	if !allowed {
		c.saved.SelectedNodeID = automaticNode(p)
	}
	c.saved.Profile = p
	c.saved.SelectedSubscriptionID = p.SubscriptionID
	c.view.DesiredSubscriptionID = p.SubscriptionID
	c.rememberLocked()
	c.restoreSelection = false
	c.bundle = b
	c.etag = newTag
	c.populateLocked(p)
	c.view.DesiredNodeID = c.saved.SelectedNodeID
	if c.saved.SelectedNodeID == "" {
		c.view.LastError = "暂无可用节点，请稍后刷新或手动选择节点"
	} else if c.view.LastError == "暂无可用节点，请稍后刷新或手动选择节点" {
		c.view.LastError = ""
	}
	now := c.clockNow()
	c.view.LastSync = &now
	c.view.ControlUnavailable = false
	c.rememberOfflineLocked(false)
	c.view.SyncError = ""
	intent := c.view.ConnectionIntent
	// Usage/lease updates also change the profile ETag. They must not cancel a
	// slow connection attempt while its effective routing configuration is stable.
	apply := intent && configurationChanged
	if apply {
		c.generation++
		c.cancelRecoveryLocked()
		if c.cancelApply != nil {
			c.cancelApply()
		}
	}
	if !intent {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
		if c.view.RunState != "failed" {
			c.view.RunState = "cached"
		}
	} else if !apply && connectionRunning(c.view.RunState) {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
	}
	e = c.persistLocked()
	c.mu.Unlock()
	if e != nil {
		return e
	}
	if apply {
		c.launch(c.applyLatest)
	} else {
		c.report()
	}
	return nil
}

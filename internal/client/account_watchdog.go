package client

import (
	"context"
	"time"
)

// Independent of account polling/backoff, enforce authorization freshness locally.
func (c *Controller) accountWatchdog(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.accountWatchdogTick(ctx)
		}
	}
}

func (c *Controller) accountWatchdogTick(ctx context.Context) {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if !c.view.ConnectionIntent || c.closed {
		c.mu.Unlock()
		return
	}
	now := c.clockNow()
	selected := c.view.Subscription
	if c.view.SelectionPending {
		selected = nil
		for _, item := range c.view.Subscriptions {
			if item.ID == c.view.DesiredSubscriptionID {
				selected = &item
				break
			}
		}
	}
	message := ""
	if selected != nil && !selected.ExpiresAt.After(now) {
		message = "当前订阅已到期，VPN 已关闭，请手动选择其他订阅"
	}
	if c.saved.Offline != nil && c.saved.Offline.SubscriptionID == c.saved.SelectedSubscriptionID && c.saved.Offline.NodeID == c.saved.SelectedNodeID {
		if !c.offlineValidLocked() && (c.view.ControlUnavailable || now.Before(c.saved.Offline.LastWall) || !now.Before(c.saved.Offline.Until)) && message == "" {
			message = "离线许可已到期或失效，VPN 已关闭，请恢复联网后重新连接"
		}
		if c.offlineValidLocked() && message == "" {
			c.saved.Offline.LastWall = now
			if err := c.persistLocked(); err != nil {
				message = "离线授权无法保存，VPN 已关闭"
			} else {
				c.mu.Unlock()
				return
			}
		}
	}
	lastContact := c.accountContact
	if c.view.LastSync != nil && c.view.LastSync.After(lastContact) {
		lastContact = *c.view.LastSync
	}
	stale := lastContact.IsZero() || now.Sub(lastContact) >= 120*time.Second
	if stale && !c.recoveryReady && message == "" {
		message = "账户服务失联超过 120 秒，VPN 已关闭，请恢复后手动重连"
	}
	if message != "" {
		c.stopStateLocked(message)
		c.mu.Unlock()
		_ = c.engine.Stop()
		return
	}
	if !stale || c.suspended {
		c.mu.Unlock()
		return
	}
	c.startRecoveryLocked("authorization_stale")
	r := c.recovery
	c.mu.Unlock()
	if r == nil {
		return
	}
	err := c.engine.SetSuspended(r.ctx, true)
	c.mu.Lock()
	current := c.recoveryCurrentLocked(r)
	if current {
		if err == nil {
			c.suspended = true
		} else {
			c.stopStateLocked("暂停代理转发失败，VPN 已关闭，请手动重新连接")
		}
	}
	c.mu.Unlock()
	if current && err != nil {
		_ = c.engine.Stop()
	}
}

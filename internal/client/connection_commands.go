package client

import (
	"context"
	"errors"
)

func (c *Controller) Connect() error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.api == nil {
		c.mu.Unlock()
		return errors.New("请先登录用户账户")
	}
	// Connect expresses intent, not a forced restart. Repeated clicks from a
	// delayed view must preserve an in-flight apply or same-node recovery.
	if c.view.ConnectionIntent {
		c.mu.Unlock()
		return nil
	}
	worker, err := c.connectLocalChoiceLocked()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if worker != nil {
		c.mu.Unlock()
		c.launch(worker)
		return nil
	}
	if c.view.SelectionPending {
		c.mu.Unlock()
		return errors.New("正在切换订阅，请稍后连接")
	}
	if c.view.DesiredSubscriptionID != c.saved.SelectedSubscriptionID {
		c.mu.Unlock()
		return errors.New("订阅切换尚未完成，请重新选择订阅后再连接")
	}
	if !usableSubscription(c.saved.Profile) {
		c.mu.Unlock()
		return errors.New("请先选择一份有效订阅；已到期或耗尽的订阅不可连接")
	}
	if len(c.bundle.Rules) == 0 {
		c.mu.Unlock()
		return errors.New("订阅尚未完整同步，请先刷新")
	}
	if c.saved.Offline != nil && !c.offlineValidLocked() {
		c.saved.Offline = nil
		c.view.OfflineUntil = nil
	}
	c.view.ConnectionIntent = true
	c.view.LastError = ""
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	c.launch(c.applyLatest)
	return nil
}

func (c *Controller) Select(id string) error {
	c.mu.Lock()
	subscription := c.saved.SelectedSubscriptionID
	if choice := c.localChoiceLocked(); choice != nil {
		subscription = choice.SubscriptionID
	}
	c.mu.Unlock()
	return c.stageLocalChoice(subscription, id)
}

func (c *Controller) Disconnect() error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.mu.Lock()
	// Revoke queued switching before waiting for route work. Canceled remote
	// mutations remain uncertain until the next connection revalidates them.
	c.view.ConnectionIntent = false
	c.generation++
	c.selectionGeneration++
	c.view.SelectionPending = false
	c.cancelDelayLocked()
	if c.cancelSelection != nil {
		c.cancelSelection()
		c.cancelSelection = nil
	}
	if c.recovery != nil {
		c.recovery.cancel()
	}
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	c.view.ConnectionIntent = false
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.mu.Unlock()
	e := c.engine.Stop()
	c.mu.Lock()
	c.view.ActualNodeID = ""
	c.view.ActualSubscriptionID = ""
	c.view.RunState = "disconnected"
	c.view.LastError = ""
	c.mu.Unlock()
	// Local shutdown must not hold routeMu while waiting for an account request
	// (possibly queued behind a slow subscription switch). Close joins this
	// tracked worker; reportMu still serializes and snapshots current status.
	c.launch(c.report)
	return e
}
func (c *Controller) SetMode(mode string, tun bool) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	if mode != "rule" && mode != "global" {
		return errors.New("模式无效")
	}
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	// The dashboard, settings and tray share this entry point. Selecting the
	// current mode is idempotent and must not drain an active node lease.
	if c.saved.Mode == mode && c.saved.TUN == tun {
		c.mu.Unlock()
		return nil
	}
	previousMode, previousTUN := c.saved.Mode, c.saved.TUN
	c.saved.Mode = mode
	c.saved.TUN = tun
	if err := c.persistLocked(); err != nil {
		c.saved.Mode, c.saved.TUN = previousMode, previousTUN
		c.mu.Unlock()
		return err
	}
	c.view.Mode = mode
	c.view.TUNEnabled = tun
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	on := c.view.ConnectionIntent
	c.mu.Unlock()
	if on {
		c.launch(c.applyLatest)
	}
	return nil
}

package client

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Recovery belongs to one successfully applied connection, never a new choice.
type recoveryRun struct {
	api                   *API
	generation, selection uint64
	subscription, node    string
	ctx                   context.Context
	cancel                context.CancelFunc
}

type temporaryFailure struct{ error }

func (e *temporaryFailure) Unwrap() error { return e.error }

func temporary(err error) error { return &temporaryFailure{err} }
func retryable(err error) bool {
	var transient *temporaryFailure
	return errors.As(err, &transient) || errors.Is(err, context.DeadlineExceeded)
}

func connectionRunning(state string) bool { return state == "connected" || state == "recovering" }

func (c *Controller) clockNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Controller) cancelRecoveryLocked() {
	if c.recovery != nil {
		c.recovery.cancel()
		c.recovery = nil
	}
	c.recoveryReady = false
}

func (c *Controller) recoveryCurrentLocked(r *recoveryRun) bool {
	return !c.closed && c.recovery == r && r.ctx.Err() == nil && c.api == r.api &&
		c.generation == r.generation && c.selectionGeneration == r.selection &&
		c.view.ConnectionIntent && !c.view.SelectionPending && c.recoveryReady &&
		c.saved.SelectedSubscriptionID == r.subscription && c.saved.SelectedNodeID == r.node
}

func (c *Controller) startRecoveryLocked(reason string) {
	if c.closed || !c.recoveryReady || !c.view.ConnectionIntent || c.view.SelectionPending || c.api == nil || c.recovery != nil {
		return
	}
	ctx, cancel := context.WithCancel(c.workCtx)
	r := &recoveryRun{c.api, c.generation, c.selectionGeneration, c.saved.SelectedSubscriptionID, c.saved.SelectedNodeID, ctx, cancel}
	c.recovery = r
	c.view.RunState = "recovering"
	c.view.ActualNodeID, c.view.ActualSubscriptionID = "", ""
	c.view.LastError, c.view.SyncError = "", ""
	c.view.Metrics.UploadRate, c.view.Metrics.DownloadRate = 0, 0
	c.view.Metrics.DelayMS, c.view.Metrics.DelayAt = nil, nil
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer cancel()
		c.recoveryLog("started", reason, 0)
		c.recoverConnection(r)
	}()
}

func recoveryDelay(attempt int, random float64) time.Duration {
	delays := [...]time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second}
	return time.Duration(float64(delays[min(attempt, len(delays)-1)]) * (0.8 + 0.4*random))
}

func (c *Controller) recoverConnection(r *recoveryRun) {
	for attempt := 0; ; attempt++ {
		done, err := c.recoveryAttempt(r)
		if r.ctx.Err() != nil {
			return
		}
		if done {
			c.report()
			return
		}
		if err != nil && !retryable(err) {
			c.stopRecovery(r, err.Error())
			c.report()
			return
		}
		c.recoveryLog("retry", "network_or_lease_unavailable", attempt+1)
		timer := time.NewTimer(recoveryDelay(attempt, rand.Float64()))
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func matchingLease(state Connections, subscription, node string, now time.Time) bool {
	for _, lease := range state.Connections {
		if lease.State == "active" && lease.SubscriptionID == subscription && lease.NodeID == node && lease.ExpiresAt.After(now) {
			return true
		}
	}
	return false
}

func subscriptionFailure(p Profile, now time.Time) error {
	if p.Subscription == nil || p.SubscriptionID == "" || p.Subscription.ID != p.SubscriptionID {
		return errors.New("当前订阅授权已移除，VPN 已关闭")
	}
	s := p.Subscription
	if !s.ExpiresAt.After(now) {
		return errors.New("当前订阅已到期，VPN 已关闭")
	}
	if s.QuotaBytes != nil && s.UsedBytes >= *s.QuotaBytes {
		return errors.New("当前订阅流量额度已耗尽，VPN 已关闭")
	}
	if s.Status != "active" {
		return errors.New("当前订阅已停用，VPN 已关闭")
	}
	return nil
}

func (c *Controller) recoveryAttempt(r *recoveryRun) (bool, error) {
	c.mu.Lock()
	current := c.recoveryCurrentLocked(r)
	c.mu.Unlock()
	if !current {
		return true, nil
	}
	// Sync remains the authority for revocations, rules and credential changes.
	if err := c.Sync(r.ctx); err != nil {
		return false, err
	}
	c.mu.Lock()
	if !c.recoveryCurrentLocked(r) {
		c.mu.Unlock()
		return true, nil
	}
	p := c.saved.Profile
	now := c.clockNow()
	fresh := c.view.LastSync != nil && now.Sub(*c.view.LastSync) < 120*time.Second
	var node Node
	for _, n := range p.Nodes {
		if n.ID == r.node {
			node = n
			break
		}
	}
	c.mu.Unlock()
	if err := subscriptionFailure(p, now); err != nil {
		return false, err
	}
	if node.ID == "" {
		return false, errors.New("所选节点授权已移除，VPN 已关闭")
	}
	if !fresh || !node.Ready {
		return false, temporary(errors.New("等待账户或节点恢复"))
	}
	if !c.engine.Alive() {
		return false, errors.New("代理内核已停止，请手动重新连接")
	}
	// Never drain or steal another device's slot during recovery.
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	before, err := r.api.Connections(ctx)
	cancel()
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			_ = c.expireSession(r.api)
		}
		return false, err
	}
	if before.MaxConnections > 0 && before.Occupied >= before.MaxConnections && !matchingLease(before, r.subscription, r.node, c.clockNow()) {
		return false, temporary(errors.New("等待连接名额"))
	}
	c.recoveryProbeMu.Lock()
	if r.ctx.Err() != nil {
		c.recoveryProbeMu.Unlock()
		return true, nil
	}
	probeCtx, probeCancel := context.WithTimeout(r.ctx, 25*time.Second)
	err = c.engine.VerifyCurrent(probeCtx, node.PublicIP)
	probeCancel()
	c.recoveryProbeMu.Unlock()
	if err != nil {
		return false, err
	}
	ctx, cancel = context.WithTimeout(r.ctx, 5*time.Second)
	state, err := r.api.Connections(ctx)
	cancel()
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			_ = c.expireSession(r.api)
		}
		return false, err
	}
	// A successful probe alone is insufficient: admission must be confirmed.
	if state.SubscriptionID != r.subscription || state.SelectionRevision != p.SelectionRevision ||
		!matchingLease(state, r.subscription, r.node, c.clockNow()) {
		return false, temporary(errors.New("等待连接授权确认"))
	}
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if !c.recoveryCurrentLocked(r) {
		c.mu.Unlock()
		return true, nil
	}
	if c.view.LastSync == nil || c.clockNow().Sub(*c.view.LastSync) >= 120*time.Second {
		c.mu.Unlock()
		return false, temporary(errors.New("等待账户恢复"))
	}
	suspended := c.suspended
	c.mu.Unlock()
	if suspended {
		if err := c.engine.SetSuspended(r.ctx, false); err != nil {
			return false, errors.New("恢复代理转发失败，请手动重新连接")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.recoveryCurrentLocked(r) {
		return true, nil
	}
	if err := subscriptionFailure(c.saved.Profile, c.clockNow()); err != nil {
		return false, err
	}
	c.suspended = false
	c.view.RunState = "connected"
	c.view.ActualNodeID, c.view.ActualSubscriptionID = r.node, r.subscription
	c.view.Connections = state
	c.view.LastError, c.view.SyncError = "", ""
	c.recovery = nil
	c.recoveryLog("restored", "verified", 0)
	return true, nil
}

func (c *Controller) stopRecovery(r *recoveryRun, message string) {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if !c.recoveryCurrentLocked(r) {
		c.mu.Unlock()
		return
	}
	c.stopStateLocked(message)
	c.mu.Unlock()
	_ = c.engine.Stop()
	c.recoveryLog("stopped", "terminal_failure", 0)
}

func (c *Controller) stopStateLocked(message string) {
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.view.ConnectionIntent = false
	c.view.ActualNodeID, c.view.ActualSubscriptionID = "", ""
	c.view.RunState, c.view.LastError = "failed", message
	c.view.SyncError = ""
}

// Every terminal asynchronous result must still belong to this connection.
func (c *Controller) failGeneration(generation uint64, message string) {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.closed || c.generation != generation {
		c.mu.Unlock()
		return
	}
	c.stopStateLocked(message)
	c.mu.Unlock()
	_ = c.engine.Stop()
}

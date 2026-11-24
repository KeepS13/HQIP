package client

import (
	"context"
	"errors"
	"time"
)

func (c *Controller) applyLatest() {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	if c.closed || !c.view.ConnectionIntent || c.api == nil {
		c.mu.Unlock()
		return
	}
	if c.view.RunState == "connected" && c.appliedGeneration == c.generation {
		c.mu.Unlock()
		return
	}
	g := c.generation
	p := c.saved.Profile
	b := c.bundle
	id := c.saved.SelectedNodeID
	mode := c.saved.Mode
	tun := c.saved.TUN
	server := c.saved.ServerURL
	api := c.api
	offlineReconnect := c.offlineValidLocked()
	if c.view.SelectionPending || !usableSubscription(p) {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 135*time.Second)
	c.cancelDelayLocked()
	c.cancelApply = cancel
	c.view.RunState = "connecting"
	c.view.LastError = ""
	c.mu.Unlock()
	// Release the old transport before any candidate consumes another slot.
	e := c.engine.Stop()
	if e == nil && id == "" {
		e = errors.New("暂无可用节点，请稍后刷新或手动选择节点")
	}
	if e == nil && !offlineReconnect {
		e = api.Drain(ctx, p.SubscriptionID, p.SelectionRevision)
	}
	if e == nil && !offlineReconnect {
		e = api.WaitDrained(ctx, p.SubscriptionID, p.SelectionRevision)
	}
	if e == nil {
		e = c.engine.Apply(ctx, p, b, id, mode, tun, server)
	}
	if e != nil && !errors.Is(e, ErrUnauthorized) && ctx.Err() == nil {
		if state, err := api.Connections(ctx); errors.Is(err, ErrUnauthorized) {
			e = err
		} else if err == nil && state.Message != "" {
			e = errors.New(state.Message)
		}
	}
	cancel()
	if errors.Is(e, ErrUnauthorized) {
		_ = c.expireSession(api)
		return
	}
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	var measure func() error
	if e != nil {
		_ = c.engine.Stop()
	}
	c.mu.Lock()
	if g != c.generation || !c.view.ConnectionIntent || c.closed {
		c.mu.Unlock()
		_ = c.engine.Stop()
		return
	}
	if e != nil {
		c.view.RunState = "failed"
		c.view.ConnectionIntent = false
		c.view.ActualNodeID = ""
		c.view.ActualSubscriptionID = ""
		c.view.LastError = e.Error()
		c.view.ProcessedConfigRevision = p.Revision
	} else {
		c.view.AppliedConfigRevision = p.Revision
		c.view.ProcessedConfigRevision = p.Revision
		c.view.ActualNodeID = id
		c.view.ActualSubscriptionID = p.SubscriptionID
		c.view.RunState = "connected"
		c.recoveryReady = true
		c.suspended = false
		c.appliedGeneration = g
		c.view.LastError = ""
		c.beginMetricsLocked()
		c.rememberOfflineLocked(true)
		if err := c.persistLocked(); err != nil {
			c.stopStateLocked("连接授权无法保存，请重试")
			e = err
		} else {
			// Reserve the one measurement while this generation is current.
			// The HTTP probe runs after unlocking and never delays connection UI.
			measure, _ = c.prepareDelayLocked()
		}
	}
	c.mu.Unlock()
	if e != nil {
		_ = c.engine.Stop()
	}
	if measure != nil {
		c.launch(func() { _ = measure() })
	}
	c.report()
}
func (c *Controller) report() {
	if !c.beginWork() {
		return
	}
	defer c.workers.Done()
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.mu.Lock()
	api := c.api
	s := c.view.Status
	unchanged := c.lastReportAPI == api && c.lastReport == s
	c.mu.Unlock()
	if api == nil || unchanged || c.workCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Second)
	defer cancel()
	err := api.Report(ctx, s)
	if errors.Is(err, ErrUnauthorized) {
		// Some callers hold routeMu until reporting finishes. Clean up in a
		// tracked worker so expiry cannot deadlock that route operation.
		c.launch(func() { _ = c.expireSession(api) })
		return
	}
	c.mu.Lock()
	if err == nil && c.api == api && !c.closed {
		c.lastReportAPI, c.lastReport = api, s
	}
	c.mu.Unlock()
}

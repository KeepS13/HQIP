package client

import (
	"context"
	"errors"
	"time"
)

// Caller holds mu. Only one current-connection measurement may be in flight,
// whether requested manually or reserved by a successful Apply. No catalog
// probe or account request is involved. The returned job runs without mu.
func (c *Controller) prepareDelayLocked() (func() error, error) {
	engine, ok := c.engine.(delayEngine)
	if !ok {
		return nil, errors.New("当前内核不支持延迟检测")
	}
	// Disconnect revokes intent before waiting for routeMu/engine shutdown.
	// The old connected label alone must not admit new work during that gap.
	if !c.view.ConnectionIntent || c.view.RunState != "connected" || c.view.ActualNodeID == "" || c.view.ActualSubscriptionID == "" || c.view.SelectionPending || c.view.Metrics.DelayPending {
		return nil, errors.New("请连接当前节点后再测试延迟")
	}
	g := c.generation
	ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Second)
	c.cancelDelay = cancel
	c.view.Metrics.DelayPending = true
	c.view.Metrics.DelayMS, c.view.Metrics.DelayAt = nil, nil
	return func() error {
		defer cancel()
		var delay int64
		err := ctx.Err()
		if err == nil {
			delay, err = engine.Delay(ctx)
		}
		if err == nil {
			err = ctx.Err()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || g != c.generation || !c.view.ConnectionIntent || c.view.RunState != "connected" || ctx.Err() == context.Canceled {
			return nil // A late result never labels a newer node, even if it ignored cancellation.
		}
		c.cancelDelay = nil
		c.view.Metrics.DelayPending = false
		if err != nil {
			return err
		}
		now := time.Now()
		c.view.Metrics.DelayMS, c.view.Metrics.DelayAt = &delay, &now
		return nil
	}, nil
}

// Clearing the previous result and canceling its request is atomic with the
// connection generation change. Caller holds mu; cancellation never waits.
func (c *Controller) cancelDelayLocked() {
	if c.cancelDelay != nil {
		c.cancelDelay()
		c.cancelDelay = nil
	}
	c.view.Metrics.DelayPending = false
	c.view.Metrics.DelayMS, c.view.Metrics.DelayAt = nil, nil
}

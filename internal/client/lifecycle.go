package client

import "context"

// Registration shares the closed gate with Close, so no worker can be added
// after shutdown starts waiting for the last accepted operation.
func (c *Controller) beginWork() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.workers.Add(1)
	return true
}

func (c *Controller) launch(work func()) bool {
	if !c.beginWork() {
		return false
	}
	go func() {
		defer c.workers.Done()
		work()
	}()
	return true
}

// Keep the caller's deadline while also canceling requests when the
// controller closes. This includes Run calls with a non-cancelable parent.
func (c *Controller) workContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.workCtx, cancel)
	if c.workCtx.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

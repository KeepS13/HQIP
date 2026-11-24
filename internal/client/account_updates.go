package client

import (
	"context"
	"errors"
	"time"
)

// Run keeps local health work independent from account I/O. Selection never
// wakes account sync. Restored sessions bootstrap once; browser login already
// completed that import. Business changes and an expiring active authorization
// are the only automatic reasons to import again.
func (c *Controller) Run(ctx context.Context) {
	if !c.beginWork() {
		return
	}
	defer c.workers.Done()
	ctx, cancel := c.workContext(ctx)
	defer cancel()
	c.launch(func() { c.accountWatchdog(ctx) })
	c.launch(func() { c.runMetrics(ctx) })
	c.launch(func() { c.runAccountUpdates(ctx) })
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.mu.Lock()
		active := c.view.ConnectionIntent || connectionRunning(c.view.RunState)
		aliveExpected, generation := connectionRunning(c.view.RunState), c.generation
		c.mu.Unlock()
		if !active {
			continue
		} // Idle clients do no lease/billing/forwarding requests.
		if aliveExpected && !c.engine.Alive() {
			c.failGeneration(generation, "代理内核已停止，请手动重新连接")
			c.report()
		}
		c.checkAccountConnection(ctx)
		c.checkOfflineForwarding(ctx)
	}
}

func (c *Controller) runAccountUpdates(ctx context.Context) {
	for ctx.Err() == nil {
		c.mu.Lock()
		api := c.api
		if api == nil {
			c.mu.Unlock()
			if !c.waitAccountUpdate(ctx, time.Second) {
				return
			}
			continue
		}
		watchCtx, cancel := context.WithCancel(ctx)
		c.accountCancel = cancel // Logout cancels reads before remote revocation.
		c.mu.Unlock()
		c.watchAccount(watchCtx, api)
		cancel()
		if !c.waitAccountUpdate(ctx, time.Second) {
			return
		}
	}
}

func (c *Controller) watchAccount(ctx context.Context, api *API) {
	backoff := time.Second
	unsupported := false
	for ctx.Err() == nil {
		c.mu.Lock()
		current, imported := c.api == api && !c.closed, c.syncedAPI == api
		capable := c.saved.Profile.ChangeToken != ""
		c.mu.Unlock()
		if !current {
			return
		}
		var err error
		if !imported {
			err = c.Sync(ctx)
		}
		if err == nil && imported && capable && !unsupported {
			streamStarted := time.Now()
			err = api.watchChanges(ctx, func(token string) error {
				c.mu.Lock()
				if c.api != api || c.closed {
					c.mu.Unlock()
					return context.Canceled
				}
				changed := token != c.saved.Profile.ChangeToken
				now, grant := c.clockNow(), c.saved.Offline
				// The node renews its real grant separately from catalog revisions.
				// Fetch that deadline near expiry; a heartbeat cannot grant more time.
				// If the server has not renewed yet, retry at most once per minute.
				renew := c.view.ConnectionIntent && grant != nil &&
					grant.SubscriptionID == c.saved.SelectedSubscriptionID && grant.NodeID == c.saved.SelectedNodeID &&
					!grant.Until.After(now.Add(2*time.Minute)) &&
					(c.view.LastSync == nil || !c.view.LastSync.Add(time.Minute).After(now))
				c.mu.Unlock()
				if changed || renew {
					if err := c.Sync(ctx); err != nil {
						return err
					}
				}
				c.mu.Lock()
				if c.api == api && token == c.saved.Profile.ChangeToken {
					// A heartbeat confirms the unchanged catalog, never extends an
					// offline grant or changes LastSync/selected/actual identity.
					c.accountContact = c.clockNow()
					c.view.ControlUnavailable = false
				}
				c.mu.Unlock()
				// A server that sends one line and immediately drops must not
				// reset backoff on every reconnect and produce a request loop.
				if time.Since(streamStarted) >= 30*time.Second {
					backoff = time.Second
				}
				return nil
			})
			if errors.Is(err, errChangesUnsupported) {
				unsupported, err = true, nil
			}
		}
		if errors.Is(err, ErrUnauthorized) {
			_ = c.expireSession(api)
			return
		}
		if !imported && err == nil {
			continue
		}
		if err != nil {
			if !c.waitAccountUpdate(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		// Older servers don't advertise the stream: no speculative endpoint
		// calls or idle polling. Active legacy transports retain a bounded 60s
		// authorization refresh; manual Refresh is always available.
		if !c.waitAccountUpdate(ctx, time.Minute) {
			return
		}
		c.mu.Lock()
		active := c.api == api && c.view.ConnectionIntent
		c.mu.Unlock()
		if active {
			_ = c.Sync(ctx)
		}
	}
}

func (c *Controller) waitAccountUpdate(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-c.wake:
		return true
	case <-timer.C:
		return true
	}
}

package client

import "context"

// Installed before publishing an API. authMu never nests inside Controller.mu.
func (c *Controller) bindTokenPersistence(api *API) {
	api.saveTokens = func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || c.api != api {
			return context.Canceled
		}
		return c.persistLocked()
	}
}

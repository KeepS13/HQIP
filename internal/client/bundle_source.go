package client

// BundleUpdateSource returns the configured public release origin without
// requiring an account. Startup and logout must not prevent a broken/old login
// flow from receiving an update. The updater validates HTTPS and the pinned
// signature; this accessor holds only mu and never performs network I/O.
func (c *Controller) BundleUpdateSource() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saved.ServerURL
}

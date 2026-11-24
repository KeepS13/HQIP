package client

// switchActiveChoice owns no network work on the selection command's stack.
// Only the latest accepted generation may consume its persisted choice. The
// existing apply/authorization workers serialize Stop -> drain -> Apply; this
// worker must never hold routeMu while waiting for their applyMu or HTTP work.
func (c *Controller) switchActiveChoice(api *API, generation uint64) {
	c.routeMu.Lock()
	c.mu.Lock()
	if c.closed || c.api != api || c.generation != generation || !c.view.ConnectionIntent {
		c.mu.Unlock()
		c.routeMu.Unlock()
		return
	}
	c.view.SelectionPending = false
	worker, err := c.connectLocalChoiceLocked()
	if err != nil {
		c.stopStateLocked("节点切换失败：" + err.Error())
		c.mu.Unlock()
		_ = c.engine.Stop()
		c.routeMu.Unlock()
		return
	}
	if c.saved.Offline != nil && !c.offlineValidLocked() {
		c.saved.Offline = nil
		c.view.OfflineUntil = nil
	}
	c.mu.Unlock()
	c.routeMu.Unlock()
	if worker != nil {
		c.launch(worker)
	} else {
		c.launch(c.applyLatest)
	}
}

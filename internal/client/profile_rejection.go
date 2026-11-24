package client

import (
	"github.com/KeepS13/HQIP/internal/rules"
)

// A rejected new profile must not leave the previous route running, even when
// that previous node remains an allowed member of the newly assigned set.
func (c *Controller) rejectProfile(api *API, p Profile, generation uint64, err error) error {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if c.api != api || c.generation != generation || c.view.SelectionPending || p.SelectionRevision < c.saved.Profile.SelectionRevision {
		c.mu.Unlock()
		return err
	}
	c.generation++
	c.cancelRecoveryLocked()
	if c.cancelApply != nil {
		c.cancelApply()
	}
	c.saved.Profile = p
	c.saved.SelectedNodeID = p.DefaultNodeID
	c.saved.SelectedSubscriptionID = p.SubscriptionID
	c.bundle = rules.Bundle{}
	c.etag = ""
	c.populateLocked(p)
	c.view.DesiredNodeID = p.DefaultNodeID
	c.view.DesiredSubscriptionID = p.SubscriptionID
	c.view.ActualNodeID = ""
	c.view.ActualSubscriptionID = ""
	c.view.ProcessedConfigRevision = p.Revision
	c.view.SyncError = err.Error()
	c.view.LastError = err.Error()
	if c.view.ConnectionIntent {
		c.view.RunState = "failed"
		c.view.ConnectionIntent = false
	}
	_ = c.persistLocked()
	c.mu.Unlock()
	_ = c.engine.Stop()
	c.report()
	return err
}

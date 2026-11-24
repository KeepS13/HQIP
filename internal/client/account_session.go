package client

import (
	"context"
	"errors"
	"github.com/KeepS13/HQIP/internal/rules"
	"os"
	"path/filepath"
	"time"
)

func (c *Controller) Logout() error {
	c.CancelLogin()
	return c.logoutFor(nil)
}

func (c *Controller) logoutFor(expected *API) error {
	return c.endSession(expected, false)
}

// Expiry belongs to the account session, not one connection generation. A
// delayed response from a replaced API must never sign out the new account.
func (c *Controller) expireSession(expected *API) error {
	if expected == nil {
		return nil
	}
	return c.endSession(expected, true)
}

func (c *Controller) endSession(expected *API, expired bool) error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	api := c.api
	if expected != nil && api != expected {
		c.mu.Unlock()
		return nil
	}
	c.generation++
	c.cancelRecoveryLocked()
	c.selectionGeneration++
	if c.accountCancel != nil {
		c.accountCancel()
		c.accountCancel = nil
	}
	c.accountContact = time.Time{}
	c.syncedAPI = nil
	if c.cancelSelection != nil {
		c.cancelSelection()
	}
	if c.cancelApply != nil {
		c.cancelApply()
	}
	if c.nodeTest != nil {
		c.nodeTest.cancel()
		c.nodeTest = nil
	}
	c.view.ConnectionIntent = false
	c.mu.Unlock()
	stopErr := c.engine.Stop()
	// Rejected credentials cannot revoke anything remotely. Do not make login
	// expiry wait on another network request or attempt a second token refresh.
	if api != nil && !expired {
		ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Second)
		_ = api.Logout(ctx)
		cancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = nil
	c.saved.Tokens = Tokens{}
	c.saved.Profile = Profile{}
	c.saved.Offline = nil
	c.view.OfflineUntil = nil
	c.view.ControlUnavailable = false
	c.saved.SelectedNodeID = ""
	c.saved.SelectedSubscriptionID = ""
	c.cancelSelection = nil
	c.cancelApply = nil
	c.restoreSelection = false
	c.suspended = false
	c.bundle = rules.Bundle{}
	c.etag = ""
	c.view = View{ServerURL: c.saved.ServerURL, Status: Status{Mode: c.saved.Mode, TUNEnabled: c.saved.TUN, RunState: "disconnected", CoreVersion: CoreVersion}}
	if expired {
		c.view.LastError = ErrUnauthorized.Error()
	}
	_ = os.Remove(filepath.Join(c.dir, "rules.json"))
	persistErr := c.persistLocked()
	if persistErr != nil {
		// If writing the logged-out state fails, remove the old encrypted session
		// rather than restoring its invalid credentials on the next launch.
		if err := os.Remove(filepath.Join(c.dir, "session.dat")); err != nil && !os.IsNotExist(err) {
			persistErr = errors.Join(persistErr, err)
		}
		_ = os.Remove(filepath.Join(c.dir, "session.tmp"))
	}
	return errors.Join(stopErr, persistErr)
}
func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		c.CancelLogin()
		c.mu.Lock()
		c.closed = true
		c.cancelWork()
		c.generation++
		c.cancelRecoveryLocked()
		c.selectionGeneration++
		if c.cancelSelection != nil {
			c.cancelSelection()
		}
		if c.cancelApply != nil {
			c.cancelApply()
		}
		c.view.ConnectionIntent = false
		c.mu.Unlock()
		_ = c.engine.Stop()
		c.workers.Wait()
	})
}

package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Stored inside the existing machine-encrypted session. A successful profile
// fetch is not enough: this exact transport must also have connected before.
type offlineAuthorization struct {
	SubscriptionID string    `json:"subscriptionId"`
	NodeID         string    `json:"nodeId"`
	SessionID      string    `json:"sessionId"`
	CredentialHash string    `json:"credentialHash"`
	RulesHash      string    `json:"rulesHash"`
	Until          time.Time `json:"until"`
	LastWall       time.Time `json:"lastWall"`
}

func credentialDigest(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

func (c *Controller) offlineValidLocked() bool {
	a := c.saved.Offline
	now := c.clockNow()
	if a == nil || c.api == nil || now.Before(a.LastWall) || !now.Before(a.Until) ||
		a.SubscriptionID != c.saved.SelectedSubscriptionID || a.NodeID != c.saved.SelectedNodeID ||
		a.SessionID != c.saved.Tokens.SessionID || a.RulesHash != c.saved.Profile.Rules.Hash ||
		len(c.bundle.Rules) == 0 || subscriptionFailure(c.saved.Profile, now) != nil {
		return false
	}
	for _, node := range c.saved.Profile.Nodes {
		if node.ID == a.NodeID && credentialDigest(node.Password) == a.CredentialHash {
			return true
		}
	}
	return false
}

func (c *Controller) rememberOfflineLocked(connected bool) {
	if c.view.ControlUnavailable || c.view.LastSync == nil {
		return
	}
	now := c.clockNow()
	if now.Before(*c.view.LastSync) || now.Sub(*c.view.LastSync) >= 120*time.Second {
		return
	}
	p := c.saved.Profile
	for _, node := range p.Nodes {
		if node.ID != c.saved.SelectedNodeID || node.OfflineAdmissionVersion != 1 {
			continue
		}
		previous := c.saved.Offline
		if !connected && node.OfflineUntil.IsZero() {
			return
		}
		if !connected && (previous == nil || previous.NodeID != node.ID || previous.SubscriptionID != p.SubscriptionID || previous.CredentialHash != credentialDigest(node.Password)) {
			return
		}
		until := c.view.LastSync.Add(24 * time.Hour)
		if !node.OfflineUntil.IsZero() && node.OfflineUntil.Before(until) {
			until = node.OfflineUntil
		}
		if p.Subscription == nil {
			return
		}
		if p.Subscription.ExpiresAt.Before(until) {
			until = p.Subscription.ExpiresAt
		}
		c.saved.Offline = &offlineAuthorization{p.SubscriptionID, node.ID, c.api.Tokens().SessionID, credentialDigest(node.Password), p.Rules.Hash, until, now}
		c.view.OfflineUntil = &until
		return
	}
}

// Check before mutating selection or stopping the current core. This request
// does not change login, selections, or authorization timestamps.
func (c *Controller) selectionAvailable(subscription, node string) error {
	c.mu.Lock()
	api, generation := c.api, c.generation
	if c.closed {
		c.mu.Unlock()
		return context.Canceled
	}
	capable := c.saved.Offline != nil
	for _, n := range c.saved.Profile.Nodes {
		capable = capable || n.OfflineAdmissionVersion == 1
	}
	changing := subscription != "" && subscription != c.saved.SelectedSubscriptionID || node != "" && node != c.saved.SelectedNodeID
	c.mu.Unlock()
	if !changing || api == nil || !capable {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Second)
	defer cancel()
	_, _, _, err := api.Profile(ctx, "")
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnauthorized) {
		_ = c.expireSession(api)
		return err
	}
	if !retryable(err) {
		if !errors.Is(err, context.Canceled) {
			c.failGeneration(generation, err.Error())
		}
		return err
	}
	c.mu.Lock()
	if api == c.api && generation == c.generation {
		c.view.ControlUnavailable = true
	}
	c.mu.Unlock()
	return errors.New("账户服务暂不可用，已保留原连接；恢复后才能切换节点或订阅")
}

// Offline recovery trusts actual forwarding through the authenticated node.
// The node, rather than a cached /connections response, enforces admission.
func (c *Controller) recoverOffline(r *recoveryRun) (bool, error) {
	c.mu.Lock()
	if !c.recoveryCurrentLocked(r) {
		c.mu.Unlock()
		return true, nil
	}
	if !c.offlineValidLocked() {
		c.mu.Unlock()
		return false, errors.New("离线许可已到期或失效，请恢复联网后重新连接")
	}
	var ip string
	for _, n := range c.saved.Profile.Nodes {
		if n.ID == r.node {
			ip = n.PublicIP
		}
	}
	c.mu.Unlock()
	if !c.engine.Alive() {
		return false, errors.New("代理内核已停止，请手动重新连接")
	}
	c.recoveryProbeMu.Lock()
	ctx, cancel := context.WithTimeout(r.ctx, 25*time.Second)
	err := c.engine.VerifyCurrent(ctx, ip)
	cancel()
	c.recoveryProbeMu.Unlock()
	if err != nil {
		return false, err
	}
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	c.mu.Lock()
	if !c.recoveryCurrentLocked(r) {
		c.mu.Unlock()
		return true, nil
	}
	if !c.offlineValidLocked() {
		c.mu.Unlock()
		return false, errors.New("离线许可已到期或失效")
	}
	suspended := c.suspended
	c.mu.Unlock()
	if suspended {
		if err = c.engine.SetSuspended(r.ctx, false); err != nil {
			return false, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.recoveryCurrentLocked(r) {
		return true, nil
	}
	c.suspended = false
	c.view.RunState = "connected"
	c.view.ActualNodeID, c.view.ActualSubscriptionID = r.node, r.subscription
	c.view.LastError = ""
	c.saved.Offline.LastWall = c.clockNow()
	err = c.persistLocked()
	if err == nil {
		c.recovery = nil
	}
	return err == nil, err
}

// A failed account refresh does not imply a failed tunnel. Probe the existing
// route separately, retaining its connected presentation while data can flow.
func (c *Controller) checkOfflineForwarding(ctx context.Context) {
	c.mu.Lock()
	if c.view.RunState != "connected" || !c.view.ConnectionIntent || !c.offlineValidLocked() {
		c.mu.Unlock()
		return
	}
	generation, api := c.generation, c.api
	ip := ""
	for _, n := range c.saved.Profile.Nodes {
		if n.ID == c.saved.SelectedNodeID {
			ip = n.PublicIP
		}
	}
	c.mu.Unlock()
	c.recoveryProbeMu.Lock()
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	err := c.engine.VerifyCurrent(probe, ip)
	cancel()
	c.recoveryProbeMu.Unlock()
	c.mu.Lock()
	current := !c.closed && c.generation == generation && c.api == api && c.view.ConnectionIntent
	if current && err != nil && retryable(err) {
		c.startRecoveryLocked("node_unavailable")
	}
	c.mu.Unlock()
	if current && err != nil && !retryable(err) && !errors.Is(err, context.Canceled) {
		c.failGeneration(generation, err.Error())
	}
}

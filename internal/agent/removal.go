package agent

import (
	"context"
	"errors"
	"os/exec"
	"sort"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type cachedRemoval struct {
	DeviceID string    `json:"deviceId"`
	Hash     string    `json:"hash"`
	Deadline time.Time `json:"deadline"`
	Revoked  bool      `json:"revoked"`
}

func cloneCache(s cachedState) cachedState {
	n := s
	if s.Retirement != nil {
		copy := *s.Retirement
		n.Retirement = &copy
	}
	if s.Certificate != nil {
		copy := *s.Certificate
		n.Certificate = &copy
	}
	n.Credentials = make(map[string]cachedCredential, len(s.Credentials))
	n.Pending = make(map[string]pendingRevocation, len(s.Pending))
	n.Removals = make(map[string]cachedRemoval, len(s.Removals))
	n.DeniedHashes = make(map[string]bool, len(s.DeniedHashes))
	for k, v := range s.Credentials {
		n.Credentials[k] = v
	}
	for k, v := range s.Pending {
		n.Pending[k] = v
	}
	for k, v := range s.Removals {
		n.Removals[k] = v
	}
	for k, v := range s.DeniedHashes {
		n.DeniedHashes[k] = v
	}
	return n
}

func beginRevoke(s *cachedState, id string, now time.Time) {
	if _, exists := s.Pending[id]; !exists {
		s.Pending[id] = pendingRevocation{Since: now.UnixNano()}
	}
}

// Authentication checks deadlines itself: it never relies on a timer having run.
func credentialDenied(s cachedState, hash string, now time.Time) bool {
	if s.DeniedHashes[hash] {
		return true
	}
	for _, r := range s.Removals {
		if r.Hash == hash && (r.Revoked || !now.Before(r.Deadline)) {
			return true
		}
	}
	return false
}

func expireRemovals(s *cachedState, now time.Time) bool {
	changed := false
	for _, r := range s.Removals {
		if r.Revoked || now.Before(r.Deadline) {
			continue
		}
		if !s.DeniedHashes[r.Hash] {
			s.DeniedHashes[r.Hash] = true
			changed = true
		}
		if _, exists := s.Pending[r.DeviceID]; !exists {
			beginRevoke(s, r.DeviceID, now)
			changed = true
		}
	}
	return changed
}

func finishRevoke(s *cachedState, deviceID string, now time.Time) {
	delete(s.Pending, deviceID)
	for id, r := range s.Removals {
		if r.DeviceID == deviceID && (s.DeniedHashes[r.Hash] || !now.Before(r.Deadline)) {
			r.Revoked = true
			s.Removals[id] = r
		}
	}
}

func advanceApplied(s *cachedState) {
	if len(s.Pending) != 0 {
		return
	}
	for _, r := range s.Removals {
		if !r.Revoked {
			return
		}
	}
	s.AppliedRevision = s.DesiredRevision
}

func removalStatuses(s cachedState) []model.AgentRemovalStatus {
	result := make([]model.AgentRemovalStatus, 0, len(s.Removals))
	for id, r := range s.Removals {
		status := "scheduled"
		if r.Revoked {
			status = "revoked"
		}
		result = append(result, model.AgentRemovalStatus{ID: id, DeviceID: r.DeviceID, Status: status, Deadline: r.Deadline.UTC()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func overdue(pending map[string]pendingRevocation, now time.Time) bool {
	for _, p := range pending {
		if now.Sub(time.Unix(0, p.Since)) >= 15*time.Second {
			return true
		}
	}
	return false
}

func (a *Agent) systemdRestart(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "restart", a.cfg.HysteriaService).Run()
}

// A broken statistics/kick endpoint cannot leave a revoked session alive forever.
// Persist the alert and retry throttle before restarting, then only acknowledge
// when a different systemd invocation proves the previous process has exited.
func (a *Agent) forceDrainIfOverdue(ctx context.Context, pending map[string]pendingRevocation, reason error) error {
	a.cacheMu.RLock()
	portal := a.cache.PortalEnabled
	a.cacheMu.RUnlock()
	if portal {
		return reason
	} // Admission core closes individual leases; never interrupt other users.
	now := a.now()
	if !overdue(pending, now) {
		return reason
	}
	a.cacheMu.Lock()
	if a.cache.LastRestart != 0 && now.Sub(time.Unix(0, a.cache.LastRestart)) < time.Minute {
		a.cacheMu.Unlock()
		return reason
	}
	next := cloneCache(a.cache)
	next.LastRestart = now.UnixNano()
	next.Warning = "设备撤销会话未能正常排空，Agent 已请求重启 Hysteria；其他设备连接会短暂中断"
	if err := a.ledger.saveCache(next); err != nil {
		a.cacheMu.Unlock()
		return err
	}
	a.cache = next
	a.cacheMu.Unlock()
	a.log.Warn(next.Warning, "reason", reason)
	// Save the last obtainable traffic sample. The ledger is never cleared.
	_ = a.sample(ctx)
	a.sampleMu.Lock()
	defer a.sampleMu.Unlock()
	restartCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	previous, err := a.generation(restartCtx)
	if err != nil {
		return errors.New("无法确认旧内核运行代次，撤销仍待确认")
	}
	if err = a.restartCore(restartCtx); err != nil {
		return errors.New("重启内核以断开撤销会话失败")
	}
	current, err := a.generation(restartCtx)
	if err != nil || current == previous {
		return errors.New("尚未确认新的内核运行代次，撤销仍待确认")
	}
	a.sampleFailed()
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	next = cloneCache(a.cache)
	for id, p := range pending {
		if current, exists := next.Pending[id]; exists && current.Since == p.Since {
			finishRevoke(&next, id, now)
		}
	}
	advanceApplied(&next)
	if err := a.ledger.saveCache(next); err != nil {
		return err
	}
	a.cache = next
	return nil
}

package agent

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"
)

func (a *Agent) drainRevocations(ctx context.Context) error {
	a.cacheMu.RLock()
	retiring := a.cache.Retirement != nil
	a.cacheMu.RUnlock()
	if retiring {
		return a.progressRetirement(ctx)
	}
	a.cacheMu.Lock()
	next := cloneCache(a.cache)
	if expireRemovals(&next, a.now()) {
		if err := a.ledger.saveCache(next); err != nil {
			a.cache.Pending, a.cache.DeniedHashes = next.Pending, next.DeniedHashes
			a.cacheMu.Unlock()
			return err
		}
		a.cache = next
	}
	revision := a.cache.DesiredRevision
	pending := make(map[string]pendingRevocation, len(a.cache.Pending))
	for id, value := range a.cache.Pending {
		pending[id] = value
	}
	a.cacheMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	var online map[string]int
	if err := a.statsJSON(ctx, http.MethodGet, "/online", nil, &online); err != nil || online == nil {
		return a.forceDrainIfOverdue(ctx, pending, errors.New("无法确认被撤销设备已离线"))
	}
	var kick []string
	now := a.now()
	for id, value := range pending {
		if online[id] != 0 {
			value.ZeroSince = 0
			kick = append(kick, id)
		} else if value.ZeroSince == 0 {
			value.ZeroSince = now.UnixNano()
		}
		pending[id] = value
	}
	if len(kick) > 0 {
		sort.Strings(kick)
		if err := a.statsJSON(ctx, http.MethodPost, "/kick", kick, nil); err != nil {
			return a.forceDrainIfOverdue(ctx, pending, err)
		}
	}
	if len(kick) > 0 && overdue(pending, now) {
		return a.forceDrainIfOverdue(ctx, pending, errors.New("持续踢出后仍有旧会话"))
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if a.cache.DesiredRevision != revision {
		return nil
	}
	next = cloneCache(a.cache)
	next.Pending = pending
	for id, value := range next.Pending {
		if value.ZeroSince != 0 && now.Sub(time.Unix(0, value.ZeroSince)) >= a.drainGrace && now.Sub(time.Unix(0, value.Since)) >= a.drainGrace {
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

func (a *Agent) revokeLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastLog := time.Time{}
	for {
		if err := a.drainRevocations(ctx); err != nil && ctx.Err() == nil && a.now().Sub(lastLog) >= time.Minute {
			a.log.Warn("设备撤销待确认，将继续禁止鉴权并重试断开", "error", err)
			lastLog = a.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.revokeWake:
		}
	}
}

func (a *Agent) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		a.observeManualCertificate()
		if err := a.ledger.maintain(a.now(), maxPendingBytes); err != nil && ctx.Err() == nil {
			a.log.Warn("清理流量历史失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

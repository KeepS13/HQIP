package agent

import (
	"context"
	"sort"
)

func (a *Agent) syncOfflineGrants(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, admissionControlTimeout)
	defer cancel()
	a.offlineMu.Lock()
	defer a.offlineMu.Unlock()
	recoveryErr := a.recoverOfflineRequests(ctx)
	states, err := a.ledger.offlineStates()
	if err != nil {
		return err
	}
	// A changed core generation or elapsed monotonic child lease proves the
	// transport ended, not its final counters. Persist this before releasing
	// slots; unknown byte allocations remain reserved in the local ledger.
	if a.nodeQuotaEnabled() {
		generation, generationErr := a.generation(ctx)
		if generationErr != nil {
			generation = ""
		}
		for i := range states {
			if !states[i].Grant.Closed {
				a.reconcileOfflineChildren(ctx, &states[i], generation)
				if err = a.ledger.saveOffline(states[i]); err != nil {
					return err
				}
			}
		}
	}
	// Only an upgraded core proves new connections use protocol billing.
	// Probe once per sweep, and only if a legacy grant still needs migration.
	migrate := a.hasIdleLegacyGrant(states) && a.coreUsesProtocolBilling(ctx)
	// Oldest unserved reports run first. Fixed ID order can starve later
	// disconnected parents whenever earlier WAN requests exhaust the sweep.
	sort.SliceStable(states, func(i, j int) bool { return states[i].RetryAt.Before(states[j].RetryAt) })
	for i := range states {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s := &states[i]
		a.cacheMu.RLock()
		allowed := a.cache.PortalEnabled && a.cache.Retirement == nil
		a.cacheMu.RUnlock()
		if !allowed {
			s.Denied = true
		}
		if migrate {
			if err = a.retireIdleLegacyGrant(ctx, s); err != nil {
				if recoveryErr == nil {
					recoveryErr = err
				}
				continue
			}
		}
		if err = a.syncOfflineState(ctx, s, false); err == nil {
			_ = a.releaseIdleOfflineSlot(ctx, s)
		}
	}
	return recoveryErr
}

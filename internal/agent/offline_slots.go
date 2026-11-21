package agent

import (
	"context"

	"github.com/KeepS13/HQIP/internal/model"
)

// A child final proves exact usage; Ended proves only transport termination.
// Both free an online slot, but only final may return delegated byte budgets.
func idleOfflineSlot(s *offlineState) bool {
	for _, child := range s.Children {
		if !child.Closed && !child.Ended {
			return false
		}
	}
	return true
}

func slotAcknowledgementValid(s *offlineState, g model.OfflineGrant, u model.OfflineUsage) bool {
	if g.ConnectionSlotsVersion < s.Grant.ConnectionSlotsVersion || g.SlotReleased && (g.NodeQuota == nil || g.ConnectionSlotsVersion != 1) {
		return false
	}
	return u.SlotAction != "release" || g.SlotReleased
}

// The caller holds offlineMu. Never change an in-flight report: replay it
// first, then persist the new intent before dispatch. Lost replies cannot
// resurrect a slot that the website has already assigned to another node.
func (a *Agent) requestOfflineSlot(ctx context.Context, s *offlineState, action string) error {
	if s.Grant.ConnectionSlotsVersion != 1 || s.Grant.NodeQuota == nil || !idleOfflineSlot(s) {
		return errOfflineDenied
	}
	if s.Pending != nil || s.SlotAction != "" {
		if err := a.syncOfflineState(ctx, s, true); err != nil {
			return err
		}
		if s.Pending != nil || s.SlotAction != "" {
			return errOfflineDenied
		}
	}
	s.SlotAction = action
	if err := a.ledger.saveOffline(*s); err != nil {
		return err
	}
	return a.syncOfflineState(ctx, s, true)
}

// A disconnected website retains the centrally held slot for cached reconnect;
// the first successful synchronization releases it once the node proves idle.
func (a *Agent) releaseIdleOfflineSlot(ctx context.Context, s *offlineState) error {
	if s.Grant.ConnectionSlotsVersion != 1 || s.Grant.NodeQuota == nil || s.Grant.Closed || s.Grant.SlotReleased || !s.Online || !idleOfflineSlot(s) {
		return nil
	}
	return a.requestOfflineSlot(ctx, s, "release")
}

// Every new connection checks control when reachable. Offline fallback is
// allowed only for a slot still held centrally; released slots need a new ACK.
func (a *Agent) prepareOfflineSlot(ctx context.Context, s *offlineState) error {
	if err := a.syncOfflineState(ctx, s, s.Grant.ConnectionSlotsVersion == 1); err != nil {
		return err
	}
	if s.Grant.SlotReleased {
		if !s.Online {
			return errOfflineDenied
		}
		if err := a.requestOfflineSlot(ctx, s, "acquire"); err != nil {
			return err
		}
	}
	if s.SlotAction != "" || s.Grant.SlotReleased {
		return errOfflineDenied
	}
	return nil
}

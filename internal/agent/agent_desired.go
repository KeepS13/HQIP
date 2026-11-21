package agent

import (
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
	"strings"
	"time"
)

func (a *Agent) applyDesired(desired model.AgentDesired) error {
	if err := a.applyLifecycle(desired); err != nil {
		return err
	}
	if desired.Revision < 0 || len(desired.Devices) > 10000 {
		return errors.New("控制服务配置无效")
	}
	credentials := make(map[string]cachedCredential, len(desired.Devices))
	seenHashes := make(map[string]bool, len(desired.Devices))
	for _, device := range desired.Devices {
		if len(device.ID) == 0 || len(device.ID) > 128 || len(device.Password) < 16 || len(device.Password) > 1024 || len(device.RemovalID) > 128 || device.RevokeAfterSeconds < 0 || device.RevokeAfterSeconds > 60 {
			return errors.New("控制服务返回无效设备凭据")
		}
		hash := passwordHash(device.Password)
		if _, duplicate := credentials[device.ID]; duplicate || seenHashes[hash] {
			return errors.New("控制服务返回重复设备凭据")
		}
		credentials[device.ID] = cachedCredential{Hash: hash, Enabled: device.Enabled && (!desired.PortalEnabled || strings.HasPrefix(device.ID, "__probe_"))}
		seenHashes[hash] = true
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if desired.Revision < a.cache.DesiredRevision || desired.Revision == a.cache.DesiredRevision && desired.NodeQuotaVersion == a.cache.NodeQuotaVersion {
		return nil
	}
	replay := desired.Revision-a.cache.DesiredRevision > 1
	next := cloneCache(a.cache)
	next.PortalEnabled = desired.PortalEnabled
	next.OfflineAdmissionVersion = desired.OfflineAdmissionVersion
	next.NodeQuotaVersion = desired.NodeQuotaVersion
	next.DesiredRevision, next.Credentials = desired.Revision, credentials
	now := a.now()
	for _, device := range desired.Devices {
		if device.RemovalID == "" {
			continue
		}
		hash := credentials[device.ID].Hash
		removal, exists := next.Removals[device.RemovalID]
		if exists && (removal.DeviceID != device.ID || removal.Hash != hash) {
			return errors.New("撤销指令标识不可重复用于其他凭据")
		}
		if !exists {
			// Every removal has a maximum 60-second local grace, regardless of
			// later control revisions. Ordinary credentials have no expiration.
			seconds := device.RevokeAfterSeconds
			if seconds == 0 && device.Enabled {
				seconds = 60
			}
			removal = cachedRemoval{DeviceID: device.ID, Hash: hash, Deadline: now.Add(time.Duration(seconds) * time.Second)}
		}
		if !device.Enabled && removal.Deadline.After(now) {
			removal.Deadline = now
		}
		next.Removals[device.RemovalID] = removal
	}
	for id, old := range a.cache.Credentials {
		current, exists := credentials[id]
		if old.Enabled && (!exists || !current.Enabled || current.Hash != old.Hash) {
			next.DeniedHashes[old.Hash] = true
			beginRevoke(&next, id, now)
		}
	}
	for id, credential := range next.Credentials {
		if !credential.Enabled && !next.DeniedHashes[credential.Hash] {
			next.DeniedHashes[credential.Hash] = true
			beginRevoke(&next, id, now)
		}
	}
	expireRemovals(&next, now)
	advanceApplied(&next)
	if err := a.ledger.saveCache(next); err != nil {
		// No grant is installed on disk failure. Removals still fail closed in
		// memory; their revision is not acknowledged until persistence succeeds.
		a.cache.Pending, a.cache.Removals, a.cache.DeniedHashes = next.Pending, next.Removals, next.DeniedHashes
		return err
	}
	a.cache = next
	if replay {
		// Restore increments the revision epoch. Re-send already acknowledged
		// counters immediately so the restored control DB catches up from this node.
		if err := a.ledger.replay(a.now()); err != nil {
			return err
		}
	}
	select {
	case a.revokeWake <- struct{}{}:
	default:
	}
	return nil
}

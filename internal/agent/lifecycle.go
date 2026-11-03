package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var managedID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type cachedRetirement struct {
	ID                string `json:"id"`
	FinalizeRequested bool   `json:"finalizeRequested"`
	Stopped           bool   `json:"stopped"`
	Final             bool   `json:"final"`
}

func (a *Agent) applyLifecycle(desired model.AgentDesired) error {
	a.cacheMu.RLock()
	revision := a.cache.DesiredRevision
	retired := a.cache.Retirement != nil
	a.cacheMu.RUnlock()
	if desired.Revision < revision {
		return nil
	}
	if desired.Retirement != nil {
		if !a.cfg.Managed || !managedID.MatchString(desired.Retirement.ID) {
			return errors.New("无效或不支持的节点停止指令")
		}
		a.cacheMu.Lock()
		next := cloneCache(a.cache)
		if next.Retirement != nil && next.Retirement.ID != desired.Retirement.ID {
			a.cacheMu.Unlock()
			return errors.New("节点停止指令标识不允许更换")
		}
		if next.Retirement == nil {
			next.Retirement = &cachedRetirement{ID: desired.Retirement.ID}
		}
		next.Retirement.FinalizeRequested = next.Retirement.FinalizeRequested || desired.Retirement.Finalize
		for id, credential := range next.Credentials {
			next.DeniedHashes[credential.Hash] = true
			beginRevoke(&next, id, a.now())
		}
		if err := a.ledger.saveCache(next); err != nil {
			a.cache.Retirement = next.Retirement
			a.cacheMu.Unlock()
			return err
		}
		a.cache = next
		a.cacheMu.Unlock()
		retired = true
	}
	if !retired && desired.Certificate != nil {
		return a.applyCertificate(*desired.Certificate)
	}
	return nil
}

func (a *Agent) verifyOwnInstallation() error {
	if !a.cfg.Managed || !managedID.MatchString(a.cfg.NodeID) || a.cfg.NodeDir != "/etc/hqip-nodes/"+a.cfg.NodeID || a.cfg.StatePath != "/var/lib/hqip-nodes/"+a.cfg.NodeID+"/state.db" || a.cfg.HysteriaService != "hqip-hysteria-"+a.cfg.NodeID+".service" || a.cfg.AgentService != "hqip-agent-"+a.cfg.NodeID+".service" {
		return errors.New("节点实例路径或服务归属无效，拒绝停止")
	}
	var owner struct {
		NodeID, ControllerID, PublicIP string
		InstallationVersion            int
	}
	b, err := os.ReadFile(filepath.Join(a.cfg.NodeDir, "owner.json"))
	if err != nil || json.Unmarshal(b, &owner) != nil || owner.NodeID != a.cfg.NodeID || owner.ControllerID != a.cfg.ControllerID || owner.PublicIP != a.cfg.PublicIP || owner.InstallationVersion != 2 {
		return errors.New("节点归属记录不匹配，拒绝停止")
	}
	return nil
}

func (a *Agent) stopOwnedCore(ctx context.Context) error {
	if err := a.verifyOwnInstallation(); err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "systemctl", "stop", a.cfg.HysteriaService).Run(); err != nil {
		return errors.New("停止本节点内核失败")
	}
	if err := exec.CommandContext(ctx, "systemctl", "disable", a.cfg.HysteriaService).Run(); err != nil {
		return errors.New("禁用本节点内核失败")
	}
	output, err := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState", "--value", a.cfg.HysteriaService).Output()
	if err != nil || (strings.TrimSpace(string(output)) != "inactive" && strings.TrimSpace(string(output)) != "failed") {
		return errors.New("尚未确认节点内核停止")
	}
	return nil
}

func (a *Agent) disableOwnedAgent(ctx context.Context) error {
	if err := a.verifyOwnInstallation(); err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "systemctl", "disable", a.cfg.AgentService).Run(); err != nil {
		return errors.New("禁用本节点 Agent 失败")
	}
	return nil
}

func (a *Agent) progressRetirement(ctx context.Context) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.cacheMu.RLock()
	retirement := a.cache.Retirement
	if retirement != nil {
		copy := *retirement
		retirement = &copy
	}
	a.cacheMu.RUnlock()
	if retirement == nil {
		return nil
	}
	if !retirement.Stopped {
		_ = a.sample(ctx)
		stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := a.stopCore(stopCtx)
		cancel()
		if err != nil {
			return err
		}
		a.sampleFailed()
		a.cacheMu.Lock()
		next := cloneCache(a.cache)
		next.Retirement.Stopped = true
		for id := range next.Pending {
			finishRevoke(&next, id, a.now())
		}
		advanceApplied(&next)
		err = a.ledger.saveCache(next)
		if err == nil {
			a.cache = next
		}
		a.cacheMu.Unlock()
		if err != nil {
			return err
		}
		retirement.Stopped = true
	}
	if retirement.FinalizeRequested && !retirement.Final {
		pending, err := a.ledger.batch()
		if err != nil {
			return err
		}
		if len(pending.daily) != 0 || len(pending.minutes) != 0 {
			// Offline nodes can have many report batches. Keep the Agent alive
			// until every cumulative bucket has been durably acknowledged.
			return nil
		}
		if err := a.disableAgent(ctx); err != nil {
			return err
		}
		if err := a.prepareRetirement(ctx); err != nil {
			return err
		}
		a.cacheMu.Lock()
		defer a.cacheMu.Unlock()
		next := cloneCache(a.cache)
		next.Retirement.Final = true
		next.Credentials = map[string]cachedCredential{}
		next.Pending = map[string]pendingRevocation{}
		advanceApplied(&next)
		if err := a.ledger.saveCache(next); err != nil {
			return err
		}
		a.cache = next
	}
	return nil
}

func (a *Agent) acknowledgeRetirement(ctx context.Context, reported *model.RetirementStatus) error {
	if reported == nil || !reported.Final {
		return nil
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if err := a.finishRetirement(ctx); err != nil {
		return err
	}
	return nil
}

func (a *Agent) prepareOwnedRetirement(ctx context.Context) error {
	if err := a.verifyOwnInstallation(); err != nil {
		return err
	}
	if err := a.cleanOwnedFirewall(ctx); err != nil {
		return err
	}
	state := filepath.Dir(a.cfg.StatePath)
	archive := filepath.Join(state, "retired")
	if err := os.MkdirAll(archive, 0700); err != nil {
		return err
	}
	for _, name := range []string{"agent.json", "hysteria.yaml"} {
		from := filepath.Join(a.cfg.NodeDir, name)
		to := filepath.Join(archive, name)
		if _, err := os.Stat(from); err == nil {
			if err = archivePrivateFile(from, to); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, name := range []string{"certificates", "acme"} {
		from := filepath.Join(state, name)
		to := filepath.Join(archive, name)
		if _, err := os.Lstat(from); err == nil {
			if err = os.Rename(from, to); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// Preserve the owner tombstone and metering ledger. They prove which exact
	// installation was retired and permit a later SSH cleanup/recovery audit.
	for _, unit := range []string{a.cfg.HysteriaService, a.cfg.AgentService} {
		if err := archivePrivateFile(filepath.Join("/etc/systemd/system", unit), filepath.Join(archive, unit)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := exec.CommandContext(ctx, "systemctl", "daemon-reload").Run(); err != nil {
		return err
	}
	return nil
}

// ReadWritePaths creates separate bind mounts even when /etc and /var are on
// the same disk. Copy/fsync/unlink works across those mounts; rename does not.
func archivePrivateFile(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("待归档配置不是受限普通文件")
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err = atomicPrivateFile(to, data); err != nil {
		return err
	}
	if directory, e := os.Open(filepath.Dir(to)); e == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return os.Remove(from)
}

func (a *Agent) finishOwnedRetirement(ctx context.Context) error {
	if err := a.verifyOwnInstallation(); err != nil {
		return err
	}
	return exec.CommandContext(ctx, "systemctl", "--no-block", "stop", a.cfg.AgentService).Run()
}

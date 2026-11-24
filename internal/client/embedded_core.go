package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/KeepS13/HQIP/internal/rules"
)

// EmbeddedHost owns the native VPN descriptor and embedded proxy lifecycle.
// Start must return only after listeners are ready; Stop is idempotent. Native
// implementations must not log configuration because it contains credentials.
type EmbeddedHost interface {
	Start(string) error
	Stop() error
}

// EmbeddedCore reuses the same probes, metrics and authorization suspension as
// the desktop engine. It does not write executable code or launch subprocesses.
type EmbeddedCore struct {
	Core
	host          EmbeddedHost
	applyEmbedded sync.Mutex
}

func NewEmbeddedCore(dir string, platform Platform, host EmbeddedHost) *EmbeddedCore {
	return &EmbeddedCore{Core: Core{Dir: dir, Platform: platform}, host: host}
}

func (c *EmbeddedCore) Apply(ctx context.Context, p Profile, bundle rules.Bundle, selected, mode string, tun bool, server string) error {
	c.applyEmbedded.Lock()
	defer c.applyEmbedded.Unlock()
	if !tun {
		return errors.New("此平台需要启用系统 VPN")
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	probe, err := freePort()
	if err != nil {
		return err
	}
	api, err := freePort()
	if err != nil {
		return err
	}
	stats, err := freePort()
	if err != nil {
		return err
	}
	secret := randomID()
	raw, err := GenerateConfig(ConfigOptions{Profile: p, Bundle: bundle, Selected: selected, Mode: mode, TUN: true, MixedPort: port, ProbePort: probe, APIPort: api, StatsPort: stats, APISecret: secret, ServerURL: server})
	if err != nil {
		return err
	}
	// Windows process-name rules have no meaning on Android. The native VPN
	// builder excludes HQIP's UID, and protects all upstream sockets instead.
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		return err
	}
	// VpnService supplies a file descriptor without privileged kernel routing.
	// Keep TCP in the userspace stack as well as UDP; the desktop mixed stack's
	// kernel TCP return path is not portable to Android's per-UID VPN routes.
	for _, value := range config["inbounds"].([]any) {
		inbound := value.(map[string]any)
		if inbound["type"] == "tun" {
			inbound["stack"], inbound["mtu"] = "gvisor", 1500
		}
	}
	route := config["route"].(map[string]any)
	filtered := []any{}
	for _, value := range route["rules"].([]any) {
		if _, desktopOnly := value.(map[string]any)["process_name"]; !desktopOnly {
			filtered = append(filtered, value)
		}
	}
	route["rules"] = filtered
	raw, err = json.Marshal(config)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err = c.stopLocked(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = c.host.Start(string(raw)); err != nil {
		_ = c.host.Stop()
		return errors.New("VPN 内核启动失败，请检查系统授权")
	}
	c.tunStop = c.host.Stop
	c.port, c.probePort, c.apiPort, c.statsPort, c.secret = port, probe, api, stats, secret
	expected := ""
	for _, node := range p.Nodes {
		if node.ID == selected {
			expected = node.PublicIP
		}
	}
	if err = Probe(ctx, probe, expected); err != nil {
		_ = c.stopLocked()
		return err
	}
	c.lastProfile, c.controlURL = p, server
	return nil
}

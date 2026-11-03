package agent

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os/exec"
)

//go:embed firewall.py
var firewallProgram string

// FirewallProgram is shared with the SSH provisioner so deployment, rollback
// and Agent retirement use the same exact-rule ownership checks.
func FirewallProgram() string { return firewallProgram }

func (a *Agent) cleanOwnedFirewall(ctx context.Context) error {
	payload, _ := json.Marshal(map[string]any{"action": "cleanup", "nodeId": a.cfg.NodeID})
	if err := exec.CommandContext(ctx, "python3", "-c", firewallProgram, base64.StdEncoding.EncodeToString(payload)).Run(); err != nil {
		return errors.New("清理本节点自有防火墙规则失败")
	}
	return nil
}

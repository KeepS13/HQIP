package provision

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/KeepS13/HQIP/internal/agent"
	"github.com/KeepS13/HQIP/internal/model"
)

var firewallScript = agent.FirewallProgram()

type firewallRule struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Protocol string `json:"proto"`
}

func applyFirewall(ctx context.Context, rm *remote, n model.Node, acmeViaCaddy bool, realityPort int) ([]firewallRule, error) {
	rules := []firewallRule{{IP: n.PublicIP, Port: n.Port, Protocol: "udp"}}
	if realityPort > 0 {
		rules = append(rules, firewallRule{IP: n.PublicIP, Port: realityPort, Protocol: "tcp"})
	}
	if n.CertificateMode != "cloudflare" && !acmeViaCaddy {
		rules = append(rules, firewallRule{IP: n.PublicIP, Port: 80, Protocol: "tcp"})
	}
	out, err := rm.run(ctx, pythonCommand(firewallScript, map[string]any{"action": "apply", "nodeId": n.ID, "rules": rules}), nil)
	var response struct {
		Added []firewallRule `json:"added"`
		Error bool           `json:"error"`
	}
	if err == nil {
		if strings.TrimSpace(out) == "[]" {
			return []firewallRule{}, nil
		}
		err = json.Unmarshal([]byte(out), &response)
	}
	if response.Error {
		err = errors.New("部分 UFW 规则创建失败")
	}
	return response.Added, err
}

func cleanFirewall(ctx context.Context, rm *remote, nodeID string, rules []firewallRule) error {
	payload := map[string]any{"action": "cleanup", "nodeId": nodeID}
	if rules != nil {
		payload["rules"] = rules
	}
	_, err := rm.run(ctx, pythonCommand(firewallScript, payload), nil)
	return err
}

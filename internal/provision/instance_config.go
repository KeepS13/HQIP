package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
)

const installationVersion = 2

type instancePaths struct {
	Config, State, AgentUnit, CoreUnit string
}

func pathsFor(id string) instancePaths {
	return instancePaths{
		Config: "/etc/hqip-nodes/" + id, State: "/var/lib/hqip-nodes/" + id,
		AgentUnit: "hqip-agent-" + id + ".service", CoreUnit: "hqip-hysteria-" + id + ".service",
	}
}

func localPorts(n model.Node) (int, int) {
	// Legacy fixtures may omit the fields; persisted production nodes allocate
	// these ports in the server registry before provisioning starts.
	a, s := n.AuthPort, n.StatsPort
	if a == 0 {
		a = 18081
	}
	if s == 0 {
		s = 18082
	}
	return a, s
}

func instanceConfig(n model.Node, acmeViaCaddy bool, statsSecret string) ([]byte, error) {
	if !idRE.MatchString(n.ID) || net.ParseIP(n.PublicIP).To4() == nil {
		return nil, fmt.Errorf("无效节点或 IPv4")
	}
	p := pathsFor(n.ID)
	authPort, statsPort := localPorts(n)
	if authPort < 1024 || statsPort < 1024 || authPort > 65535 || statsPort > 65535 || authPort == statsPort {
		return nil, fmt.Errorf("节点本地鉴权和统计端口无效")
	}
	rules := []string{"reject(0.0.0.0/8)", "reject(10.0.0.0/8)", "reject(100.64.0.0/10)", "reject(127.0.0.0/8)", "reject(169.254.0.0/16)", "reject(172.16.0.0/12)", "reject(192.168.0.0/16)", "reject(224.0.0.0/4)", "reject(240.0.0.0/4)", "reject(::/128)", "reject(::1/128)", "reject(fc00::/7)", "reject(fe80::/10)", "reject(ff00::/8)"}
	config := map[string]any{
		"listen":       net.JoinHostPort(n.PublicIP, strconv.Itoa(n.Port)),
		"auth":         map[string]any{"type": "http", "http": map[string]any{"url": fmt.Sprintf("http://127.0.0.1:%d/auth?secret=%s", authPort, statsSecret)}},
		"trafficStats": map[string]any{"listen": fmt.Sprintf("127.0.0.1:%d", statsPort), "secret": statsSecret},
		"acl":          map[string]any{"inline": rules},
		"resolver":     map[string]any{"type": "udp", "udp": map[string]any{"addr": "1.1.1.1:53", "timeout": "4s"}},
		"outbounds":    []any{map[string]any{"name": "direct", "type": "direct", "direct": map[string]any{"mode": "4", "bindIPv4": n.PublicIP}}},
	}
	if n.CertificateMode == "cloudflare" {
		config["tls"] = map[string]any{"cert": p.State + "/certificates/current/cert.pem", "key": p.State + "/certificates/current/key.pem"}
	} else {
		host, port := n.PublicIP, 80
		if acmeViaCaddy {
			host, port = "127.0.0.1", 18080
		}
		config["acme"] = map[string]any{"domains": []string{n.Domain}, "ca": "letsencrypt", "dir": p.State + "/acme", "type": "http", "listenHost": host, "http": map[string]any{"altPort": port}}
	}
	return yaml.Marshal(config)
}

func contentPath(kind string, data []byte) string {
	h := sha256.Sum256(data)
	return "/opt/hqip-node/releases/" + hex.EncodeToString(h[:]) + "/" + kind
}

func instanceUnits(n model.Node, agentBinary, coreBinary string) (string, string) {
	p := pathsFor(n.ID)
	common := "Restart=always\nRestartSec=5\nUMask=0077\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=true\nPrivateTmp=true\nLimitNOFILE=65536\nLogNamespace=hqip\n"
	agent := "[Unit]\nDescription=HQIP node agent " + n.ID + "\nAfter=network-online.target\nWants=network-online.target\n[Service]\nType=simple\nExecStart=" + agentBinary + " --config " + p.Config + "/agent.json\n" + common + "ReadWritePaths=" + p.State + " " + p.Config + " /etc/systemd/system -/etc/ufw -/lib/ufw -/var/lib/ufw /run\n[Install]\nWantedBy=multi-user.target\n"
	core := "[Unit]\nDescription=HQIP Hysteria node " + n.ID + "\nAfter=network-online.target " + p.AgentUnit + "\nWants=network-online.target " + p.AgentUnit + "\n[Service]\nType=simple\nWorkingDirectory=" + p.State + "\nExecStart=" + coreBinary + " server --config " + p.Config + "/hysteria.yaml --log-level warn\n" + common + "ReadWritePaths=" + p.State + "\n[Install]\nWantedBy=multi-user.target\n"
	return agent, core
}

func (r *Runner) controllerIdentity() string {
	if r.options.ControllerID != "" {
		return r.options.ControllerID
	}
	h := sha256.Sum256([]byte(strings.TrimRight(r.options.ControlURL, "/")))
	return hex.EncodeToString(h[:])
}

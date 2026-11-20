package provision

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

//go:embed inspect.py
var inspectScript string

type ServerInspection struct {
	MachineID    string   `json:"machineId"`
	Platform     string   `json:"platform"`
	Architecture string   `json:"architecture"`
	BoundIPs     []string `json:"boundIps"`
}

type TakeoverCandidate struct {
	Unit       string            `json:"unit"`
	ConfigPath string            `json:"configPath"`
	BinaryPath string            `json:"binaryPath"`
	Kind       string            `json:"kind"`
	Active     bool              `json:"active"`
	Enabled    bool              `json:"enabled"`
	UnitHash   string            `json:"unitHash"`
	Files      map[string]string `json:"files"`
}

type PreflightResult struct {
	ServerInspection
	Fingerprint        string              `json:"fingerprint"`
	Conflicts          []string            `json:"conflicts"`
	TakeoverCandidates []TakeoverCandidate `json:"takeoverCandidates"`
	CanInstall         bool                `json:"canInstall"`
	CanTakeover        bool                `json:"canTakeover"`
	ExistingManaged    bool                `json:"existingManaged"`
}

type instanceOwner struct {
	NodeID              string `json:"nodeId"`
	ControllerID        string `json:"controllerId"`
	MachineID           string `json:"machineId"`
	PublicIP            string `json:"publicIp"`
	InstallationVersion int    `json:"installationVersion"`
}
type hostSocket struct {
	Protocol string   `json:"protocol"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Units    []string `json:"units"`
}
type hostInventory struct {
	ServerInspection
	FreeBytes    int64               `json:"freeBytes"`
	Sockets      []hostSocket        `json:"sockets"`
	Units        []TakeoverCandidate `json:"units"`
	Owner        *instanceOwner      `json:"owner"`
	LegacyOwner  string              `json:"legacyOwner"`
	LegacyHashes map[string]string   `json:"legacyHashes"`
}

func pythonCommand(script string, payload any) string {
	b, _ := json.Marshal(payload)
	return "python3 -c " + shellQuote(script) + " " + shellQuote(base64.StdEncoding.EncodeToString(b))
}

func (r *Runner) inventory(ctx context.Context, rm *remote, n model.Node) (hostInventory, error) {
	var inv hostInventory
	out, err := rm.run(ctx, pythonCommand(inspectScript, map[string]any{"nodeId": n.ID, "publicIp": n.PublicIP, "port": n.Port}), nil)
	if err != nil {
		return inv, errors.New("SSH 预检失败：需要 root/免密 sudo、Python 3、ip、ss、systemd、tar、curl 和 flock")
	}
	if err = json.Unmarshal([]byte(out), &inv); err != nil {
		return inv, errors.New("无法解析服务器预检结果")
	}
	if inv.MachineID == "" || (n.MachineID != "" && n.MachineID != inv.MachineID) {
		return inv, errors.New("服务器机器身份已改变，拒绝操作")
	}
	return inv, nil
}

func (r *Runner) CheckServer(ctx context.Context, server model.Server, password string, pin PinFunc) (ServerInspection, error) {
	n := model.Node{SSHHost: server.SSHHost, SSHUser: server.SSHUser, SSHPort: server.SSHPort, HostKey: server.HostKey, MachineID: server.MachineID}
	rm, err := r.connect(ctx, Input{Node: n, Password: password}, pin)
	if err != nil {
		return ServerInspection{}, err
	}
	defer rm.client.Close()
	inv, err := r.inventory(ctx, rm, n)
	return inv.ServerInspection, err
}

func (r *Runner) Inspect(ctx context.Context, in Input, log LogFunc, pin PinFunc) (PreflightResult, error) {
	validationNode := in.Node
	if validationNode.Domain == "" && validationNode.DNSMode == "cloudflare" {
		validationNode.Domain = "pending.invalid"
	}
	if err := Validate(validationNode); err != nil {
		return PreflightResult{}, err
	}
	rm, err := r.connect(ctx, in, pin)
	if err != nil {
		return PreflightResult{}, err
	}
	defer rm.client.Close()
	inv, err := r.inventory(ctx, rm, in.Node)
	if err != nil {
		return PreflightResult{}, err
	}
	return r.assess(in.Node, inv), nil
}

func (r *Runner) assess(n model.Node, inv hostInventory) PreflightResult {
	out := PreflightResult{ServerInspection: inv.ServerInspection, Conflicts: []string{}, TakeoverCandidates: []TakeoverCandidate{}}
	stable := inv
	// Free space changes with unrelated log writes between preview and approval.
	// It is still checked below, but is not an ownership precondition.
	stable.FreeBytes = 0
	b, _ := json.Marshal(stable)
	h := sha256.Sum256(b)
	out.Fingerprint = hex.EncodeToString(h[:])
	p := pathsFor(n.ID)
	if inv.Owner != nil {
		ownedIP := inv.Owner.PublicIP == n.PublicIP || (n.IPSwitch != nil && n.IPSwitch.FromIP != "" && inv.Owner.PublicIP == n.IPSwitch.FromIP)
		out.ExistingManaged = inv.Owner.NodeID == n.ID && inv.Owner.ControllerID == r.controllerIdentity() && inv.Owner.MachineID == inv.MachineID && ownedIP && inv.Owner.InstallationVersion == installationVersion
		if !out.ExistingManaged {
			out.Conflicts = append(out.Conflicts, "节点目录属于其他控制中心、IP 或安装，不允许覆盖")
		}
	}
	if inv.Platform != "ubuntu 22.04" && inv.Platform != "ubuntu 24.04" && inv.Platform != "debian 12" {
		out.Conflicts = append(out.Conflicts, "仅支持 Ubuntu 22.04/24.04、Debian 12")
	}
	if n.IPSwitch != nil && inv.Owner == nil {
		out.Conflicts = append(out.Conflicts, "原节点归属记录缺失，拒绝切换公网 IP")
	}
	if inv.Architecture != "x86_64" && inv.Architecture != "aarch64" && inv.Architecture != "arm64" {
		out.Conflicts = append(out.Conflicts, "仅支持 amd64/arm64")
	}
	if inv.FreeBytes < 256<<20 {
		out.Conflicts = append(out.Conflicts, "可用磁盘不足 256 MiB")
	}
	bound := false
	for _, ip := range inv.BoundIPs {
		if ip == n.PublicIP {
			bound = true
		}
	}
	if !bound {
		out.Conflicts = append(out.Conflicts, "选定公网 IPv4 尚未直接配置在服务器网卡")
	}
	candidateUnits := map[string]bool{}
	for _, unit := range inv.Units {
		if unit.Kind == "hqip-legacy" || unit.Kind == "native-hy2" {
			out.TakeoverCandidates = append(out.TakeoverCandidates, unit)
			candidateUnits[unit.Unit] = true
			if unit.Kind == "hqip-legacy" {
				candidateUnits["hqip-agent.service"] = true
			}
		}
	}
	authPort, statsPort := localPorts(n)
	for _, s := range inv.Sockets {
		relevant := s.Protocol == "udp" && s.Port == n.Port && addressOverlaps(s.Host, n.PublicIP)
		relevant = relevant || (s.Protocol == "tcp" && (s.Port == authPort || s.Port == statsPort) && addressOverlaps(s.Host, "127.0.0.1"))
		if n.CertificateMode != "cloudflare" && !n.ACMEViaCaddy && !n.SharedHost {
			relevant = relevant || (s.Protocol == "tcp" && s.Port == 80 && addressOverlaps(s.Host, n.PublicIP))
		}
		if !relevant {
			continue
		}
		allowed := len(s.Units) > 0
		for _, unit := range s.Units {
			if !(out.ExistingManaged && (unit == p.CoreUnit || unit == p.AgentUnit)) && !candidateUnits[unit] {
				allowed = false
			}
		}
		if !allowed {
			out.Conflicts = append(out.Conflicts, fmt.Sprintf("%s %s:%d 被不支持接管的服务占用", s.Protocol, s.Host, s.Port))
		}
	}
	out.CanInstall = len(out.Conflicts) == 0 && len(out.TakeoverCandidates) == 0
	out.CanTakeover = len(out.Conflicts) == 0 && len(out.TakeoverCandidates) > 0
	return out
}

func addressOverlaps(actual, target string) bool {
	return actual == target || actual == "0.0.0.0" || actual == "::" || actual == "*"
}

// The SSH session holds flock for the entire install/rollback. A disconnect
// releases the kernel lock; no stale PID or lock directory can block recovery.
func (rm *remote) lock(ctx context.Context) (func(), error) {
	s, err := rm.client.NewSession()
	if err != nil {
		return nil, err
	}
	input, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	output, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	command := "flock -n /run/lock/hqip-nodes.lock sh -c 'printf READY; cat >/dev/null'"
	if rm.sudo {
		command = "sudo -n sh -c " + shellQuote(command)
	}
	if err = s.Start(command); err != nil {
		s.Close()
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 5)
		_, e := io.ReadFull(output, buf)
		if e == nil && string(buf) != "READY" {
			e = errors.New("invalid lock response")
		}
		done <- e
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	case <-time.After(10 * time.Second):
		err = errors.New("lock timeout")
	}
	if err != nil {
		s.Close()
		return nil, errors.New("该服务器已有部署或删除任务正在执行")
	}
	return func() { _ = input.Close(); _ = s.Wait(); _ = s.Close() }, nil
}

func preflightError(p PreflightResult) error {
	if len(p.Conflicts) > 0 {
		return errors.New(strings.Join(p.Conflicts, "；"))
	}
	return errors.New("检测到已有 Hysteria/HQIP，请先预检并明确选择备份接管")
}

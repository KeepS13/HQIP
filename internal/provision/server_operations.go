package provision

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrSSHAuthentication = errors.New("SSH 密码认证失败，请更新托管凭据")
var ErrSSHIdentity = errors.New("SSH 主机指纹已改变，已停止连接")

// AgentUpgradeError exposes only controlled diagnostics, never remote output.
type AgentUpgradeError struct{ Code string }

func (e *AgentUpgradeError) Error() string {
	reason := map[string]string{
		"backup_directory":        "无法创建安全的备份目录，请检查路径和权限",
		"host_lock":               "无法获取服务器运维锁",
		"host_busy":               "服务器正在执行其他运维任务，请稍后重试",
		"disk_full":               "服务器磁盘空间不足",
		"permission_denied":       "服务器文件或目录权限不足",
		"artifact_checksum":       "Agent 产物校验失败",
		"agent_start_failed":      "新 Agent 启动或初始化失败，已尝试恢复原版本",
		"reality_start_failed":    "Reality 握手检查失败，已尝试恢复原 Agent；请检查目标域名连通性与 TCP 端口",
		"reality_firewall_failed": "Reality 防火墙规则创建失败，已尝试恢复原 Agent",
		"reality_port_busy":       "节点 IP 的 TCP 443 和 8443 均被占用，未停止原服务",
		"nat_unsupported":         "当前 Agent 发布版本不支持 NAT 节点，原服务未停止",
		"inactive_service":        "节点服务未运行，请先检查节点状态",
		"core_changed":            "检测到转发核心发生重启，请检查服务器状态",
		"rollback_failed":         "原 Agent 恢复失败，请立即检查节点服务和任务备份",
		"interrupted":             "远端升级任务被中断，已尝试恢复原版本",
	}[e.Code]
	if reason == "" {
		reason = "远端升级执行失败，请检查节点服务和任务备份"
	}
	return "Agent 升级失败：" + reason
}

type AgentRelease struct {
	NodeNetworkVersion      int               `json:"nodeNetworkVersion,omitempty"`
	ConnectionSlotsVersion  int               `json:"connectionSlotsVersion,omitempty"`
	NodeQuotaVersion        int               `json:"nodeQuotaVersion,omitempty"`
	TrafficBillingVersion   int               `json:"trafficBillingVersion,omitempty"`
	VLESSRealityVersion     int               `json:"vlessRealityVersion,omitempty"`
	OfflineAdmissionVersion int               `json:"offlineAdmissionVersion,omitempty"`
	SourceSHA               string            `json:"sourceSHA"`
	SHA256                  map[string]string `json:"sha256"`
}

func (r *Runner) AgentRelease() (AgentRelease, error) {
	var raw struct {
		NodeNetworkVersion      int               `json:"nodeNetworkVersion"`
		ConnectionSlotsVersion  int               `json:"connectionSlotsVersion"`
		NodeQuotaVersion        int               `json:"nodeQuotaVersion"`
		TrafficBillingVersion   int               `json:"trafficBillingVersion"`
		VLESSRealityVersion     int               `json:"vlessRealityVersion"`
		OfflineAdmissionVersion int               `json:"offlineAdmissionVersion"`
		SourceSHA               string            `json:"sourceSHA"`
		AgentSourceSHA          string            `json:"agentSourceSHA"`
		SHA256                  map[string]string `json:"sha256"`
	}
	data, err := os.ReadFile(filepath.Join(r.options.ArtifactDir, "manifest.json"))
	if err != nil {
		return AgentRelease{}, errors.New("无法读取 Agent 发布清单")
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return AgentRelease{}, err
	}
	if raw.AgentSourceSHA != "" {
		raw.SourceSHA = raw.AgentSourceSHA
	}
	if len(raw.SourceSHA) != 40 {
		return AgentRelease{}, errors.New("Agent 发布版本无效")
	}
	return AgentRelease{NodeNetworkVersion: raw.NodeNetworkVersion, ConnectionSlotsVersion: raw.ConnectionSlotsVersion, NodeQuotaVersion: raw.NodeQuotaVersion, SourceSHA: raw.SourceSHA, SHA256: raw.SHA256, OfflineAdmissionVersion: raw.OfflineAdmissionVersion, VLESSRealityVersion: raw.VLESSRealityVersion, TrafficBillingVersion: raw.TrafficBillingVersion}, nil
}

func (release AgentRelease) upgradeManifest(arch, hash string) []byte {
	data, _ := json.Marshal(map[string]any{"nodeNetworkVersion": release.NodeNetworkVersion, "connectionSlotsVersion": release.ConnectionSlotsVersion, "nodeQuotaVersion": release.NodeQuotaVersion, "agentTelemetryVersion": 1, "offlineAdmissionVersion": release.OfflineAdmissionVersion, "vlessRealityVersion": release.VLESSRealityVersion, "trafficBillingVersion": release.TrafficBillingVersion, "agentSourceSHA": release.SourceSHA, "sha256": map[string]string{"agent-linux-" + arch: hash}})
	return data
}

//go:embed managed_agent_upgrade.py
var managedAgentUpgrade string

func (r *Runner) UpgradeAgent(ctx context.Context, in Input, target, operationID string, log LogFunc, pin PinFunc) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{18}$`).MatchString(operationID) {
		return errors.New("运维任务编号无效")
	}
	release, err := r.AgentRelease()
	if err != nil {
		return err
	}
	if release.SourceSHA != target {
		return errors.New("发布版本已变化，请重新发起 Agent 升级")
	}
	rm, err := r.connect(ctx, in, pin)
	if err != nil {
		return err
	}
	defer rm.client.Close()
	inv, err := r.inventory(ctx, rm, in.Node)
	if err != nil {
		return err
	}
	if inv.Owner == nil || inv.Owner.NodeID != in.Node.ID || inv.Owner.ControllerID != r.controllerIdentity() {
		return errors.New("节点安装归属不匹配，拒绝升级")
	}
	arch := "amd64"
	if inv.Architecture == "aarch64" {
		arch = "arm64"
	} else if inv.Architecture != "x86_64" {
		return errors.New("不支持的节点架构")
	}
	binary, err := r.artifact("agent-linux-" + arch)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(binary)
	hash := hex.EncodeToString(sum[:])
	// Use the same tested upgrade/rollback implementation as manual operations.
	files := map[string][]byte{"artifacts/agent-linux-" + arch: binary, "scripts/hqip_firewall.py": []byte(firewallScript)}
	for _, name := range []string{"upgrade_node_agent.py", "upgrade_node_admission.py"} {
		data, e := os.ReadFile(filepath.Join(r.options.ArtifactDir, "..", "scripts", name))
		if e != nil {
			return errors.New("发布包缺少 Agent 升级脚本")
		}
		files["scripts/"+name] = data
	}
	files["artifacts/manifest.json"] = release.upgradeManifest(arch, hash)
	folder := "/var/lib/hqip-operations/" + operationID
	unit := "hqip-agent-upgrade-" + operationID
	readResult := func() (string, error) {
		output, e := rm.run(ctx, "cat "+shellQuote(folder+"/result.json"), nil)
		if e != nil {
			return "", nil
		}
		var receipt struct {
			VLESSRealityVersion int    `json:"vlessRealityVersion"`
			State               string `json:"state"`
			NodeID              string `json:"nodeId"`
			SourceSHA           string `json:"sourceSHA"`
			AgentSHA256         string `json:"agentSHA256"`
			Unchanged           bool   `json:"coreInvocationUnchanged"`
			ErrorCode           string `json:"errorCode"`
		}
		if e = json.Unmarshal([]byte(output), &receipt); e != nil {
			return "", errors.New("升级任务回执损坏")
		}
		if receipt.NodeID != in.Node.ID || receipt.SourceSHA != target {
			return "", errors.New("升级任务回执归属不匹配")
		}
		if receipt.State == "succeeded" && (receipt.AgentSHA256 != hash || !receipt.Unchanged || receipt.VLESSRealityVersion != release.VLESSRealityVersion) {
			return "", errors.New("Agent 升级结果校验失败")
		}
		if receipt.State == "failed" {
			return receipt.State, &AgentUpgradeError{Code: receipt.ErrorCode}
		}
		return receipt.State, nil
	}
	state, err := readResult()
	if err != nil {
		return err
	}
	if state == "succeeded" {
		return nil
	}
	active, _ := rm.run(ctx, "systemctl show --value -p ActiveState "+shellQuote(unit+".service"), nil)
	active = strings.TrimSpace(active)
	if active != "active" && active != "activating" {
		if _, err = rm.run(ctx, "umask 077; mkdir -p "+shellQuote(folder+"/artifacts")+" "+shellQuote(folder+"/scripts"), nil); err != nil {
			return errors.New("无法准备 Agent 升级目录")
		}
		if log != nil {
			log("upload", "正在传输并校验 Agent 发布产物")
		}
		files["run.py"] = []byte(managedAgentUpgrade)
		files["request.json"], _ = json.Marshal(map[string]string{"operationId": operationID, "nodeId": in.Node.ID, "sourceSHA": target})
		for name, data := range files {
			if err = rm.put(ctx, folder+"/"+name, data, "600"); err != nil {
				return errors.New("Agent 产物传输失败")
			}
		}
		// A oneshot unit otherwise makes systemd-run wait for the entire upgrade;
		// its failure would hide the diagnostic receipt as a unit-start error.
		command := "systemd-run --quiet --no-block --collect --unit=" + shellQuote(unit) + " --property=Type=oneshot --property=TimeoutStartSec=600 python3 -B " + shellQuote(folder+"/run.py")
		if _, err = rm.run(ctx, command, nil); err != nil {
			return errors.New("无法启动持久化 Agent 升级任务")
		}
	}
	if log != nil {
		log("upgrade", "正在备份账本并升级 Agent，转发核心继续运行")
	}
	for {
		state, err = readResult()
		if err != nil {
			return err
		}
		if state == "succeeded" {
			return nil
		}
		if err = wait(ctx, time.Second); err != nil {
			return err
		}
	}
}

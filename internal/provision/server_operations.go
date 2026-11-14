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

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrSSHAuthentication = errors.New("SSH 密码认证失败，请更新托管凭据")
var ErrSSHIdentity = errors.New("SSH 主机指纹已改变，已停止连接")
var ErrPasswordUncertain = errors.New("改密结果尚未确认，已保留恢复凭据，请重试核对")

func (s *Runner) ChangeServerPassword(ctx context.Context, v model.Server, old, next string, pin PinFunc) error {
	if old == "" || next == "" || len(next) > 1024 || strings.ContainsAny(next, "\r\n\x00") || strings.ContainsAny(v.SSHUser, ":\r\n\x00") {
		return errors.New("改密参数无效")
	}
	// Recovery first: an earlier attempt may have changed Linux but lost its reply.
	if _, err := s.CheckServer(ctx, v, next, pin); err == nil {
		return nil
	} else if !errors.Is(err, ErrSSHAuthentication) {
		return ErrPasswordUncertain
	}
	n := model.Node{SSHHost: v.SSHHost, SSHUser: v.SSHUser, SSHPort: v.SSHPort, HostKey: v.HostKey, MachineID: v.MachineID}
	rm, err := s.connect(ctx, Input{Node: n, Password: old}, pin)
	if err != nil {
		return ErrPasswordUncertain
	}
	defer rm.client.Close()
	unlock, err := rm.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = s.inventory(ctx, rm, n); err != nil {
		return err
	}
	// Password travels through encrypted SSH stdin, never argv or remote logs.
	if _, err = rm.run(ctx, "chpasswd", strings.NewReader(v.SSHUser+":"+next+"\n")); err != nil {
		return ErrPasswordUncertain
	}
	if _, err = s.CheckServer(ctx, v, next, pin); err != nil {
		return ErrPasswordUncertain
	}
	return nil
}

type AgentRelease struct {
	SourceSHA string            `json:"sourceSHA"`
	SHA256    map[string]string `json:"sha256"`
}

func (r *Runner) AgentRelease() (AgentRelease, error) {
	var raw struct {
		SourceSHA      string            `json:"sourceSHA"`
		AgentSourceSHA string            `json:"agentSourceSHA"`
		SHA256         map[string]string `json:"sha256"`
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
	return AgentRelease{raw.SourceSHA, raw.SHA256}, nil
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
	files := map[string][]byte{"artifacts/agent-linux-" + arch: binary}
	for _, name := range []string{"upgrade_node_agent.py", "upgrade_node_admission.py"} {
		data, e := os.ReadFile(filepath.Join(r.options.ArtifactDir, "..", "scripts", name))
		if e != nil {
			return errors.New("发布包缺少 Agent 升级脚本")
		}
		files["scripts/"+name] = data
	}
	manifest, _ := json.Marshal(map[string]any{"agentTelemetryVersion": 1, "agentSourceSHA": release.SourceSHA, "sha256": map[string]string{"agent-linux-" + arch: hash}})
	files["artifacts/manifest.json"] = manifest
	folder := "/var/lib/hqip-operations/" + operationID
	unit := "hqip-agent-upgrade-" + operationID
	readResult := func() (string, error) {
		output, e := rm.run(ctx, "cat "+shellQuote(folder+"/result.json"), nil)
		if e != nil {
			return "", nil
		}
		var receipt struct {
			State       string `json:"state"`
			NodeID      string `json:"nodeId"`
			SourceSHA   string `json:"sourceSHA"`
			AgentSHA256 string `json:"agentSHA256"`
			Unchanged   bool   `json:"coreInvocationUnchanged"`
		}
		if e = json.Unmarshal([]byte(output), &receipt); e != nil {
			return "", errors.New("升级任务回执损坏")
		}
		if receipt.NodeID != in.Node.ID || receipt.SourceSHA != target {
			return "", errors.New("升级任务回执归属不匹配")
		}
		if receipt.State == "succeeded" && (receipt.AgentSHA256 != hash || !receipt.Unchanged) {
			return "", errors.New("Agent 升级结果校验失败")
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
	if state == "failed" {
		return errors.New("上次升级已失败，请发起新的升级任务")
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
		command := "systemd-run --quiet --collect --unit=" + shellQuote(unit) + " --property=Type=oneshot --property=TimeoutStartSec=600 python3 -B " + shellQuote(folder+"/run.py")
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
		if state == "failed" {
			return errors.New("Agent 升级失败，已保留任务备份，请检查后重试")
		}
		if err = wait(ctx, time.Second); err != nil {
			return err
		}
	}
}

package provision

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/agent"
	"github.com/KeepS13/HQIP/internal/model"
)

//go:embed backup.py
var backupScript string

func (r *Runner) installInstance(ctx context.Context, in Input, log LogFunc, pin PinFunc) (res Result, err error) {
	if err = Validate(in.Node); err != nil {
		return
	}
	if log == nil {
		log = func(string, string) {}
	}
	base, e := url.Parse(r.options.ControlURL)
	if e != nil || base.Scheme != "https" || base.Host == "" {
		return res, errors.New("控制台公网地址必须是 HTTPS")
	}
	if in.Node.CertificateMode == "cloudflare" {
		if in.Certificate == nil {
			return res, errors.New("Cloudflare 节点尚未取得证书")
		}
		if _, err = agent.ValidateCertificateBundle(*in.Certificate, in.Node.Domain, time.Now()); err != nil {
			return
		}
	}
	log("preflight", "检查域名、SSH、主机身份和独立 IP 监听")
	if err = validateDNS(ctx, in.Node); err != nil {
		return
	}
	rm, e := r.connect(ctx, in, pin)
	if e != nil {
		return res, e
	}
	defer rm.client.Close()
	unlock, e := rm.lock(ctx)
	if e != nil {
		return res, e
	}
	defer unlock()
	inv, e := r.inventory(ctx, rm, in.Node)
	if e != nil {
		return res, e
	}
	pre := r.assess(in.Node, inv)
	res.MachineID = inv.MachineID
	res.AuthPort, res.StatsPort = localPorts(in.Node)
	res.InstallationVersion = installationVersion
	res.Managed = pre.ExistingManaged
	if !pre.CanInstall && !(in.Takeover && pre.CanTakeover) {
		return res, preflightError(pre)
	}
	if in.Takeover && (in.PreflightFingerprint == "" || in.PreflightFingerprint != pre.Fingerprint) {
		return res, errors.New("服务器状态已改变或缺少预检快照，请重新预检后接管")
	}
	if !in.Takeover && in.PreflightFingerprint != "" && in.PreflightFingerprint != pre.Fingerprint {
		return res, errors.New("服务器状态已改变，请重新预检")
	}
	localID, _ := os.ReadFile("/etc/machine-id")
	res.ControlOnNode, res.ACMEViaCaddy = deploymentTopology(in.Node, string(localID), inv.MachineID, runtime.GOOS)
	res.SharedHost = res.ACMEViaCaddy
	if res.ACMEViaCaddy {
		_, e = rm.run(ctx, `test -f /etc/hqip/Caddyfile && grep -Fq '127.0.0.1:18080' /etc/hqip/Caddyfile && grep -Fq '/.well-known/acme-challenge/' /etc/hqip/Caddyfile`, nil)
		if e != nil {
			return res, errors.New("原 Caddy 验证路由不存在，请切换 Cloudflare DNS 验证或使用节点独立 IP 的 HTTP 验证")
		}
	}
	// This verifies source binding before stopping any previous installation.
	out, e := rm.run(ctx, "curl --noproxy '*' --fail --silent --show-error --max-time 20 --interface "+shellQuote(in.Node.PublicIP)+" https://api.ipify.org", nil)
	if e != nil || strings.TrimSpace(out) != in.Node.PublicIP {
		return res, errors.New("选定 IPv4 的直接出站验证失败，未修改网卡或路由")
	}
	arch := "amd64"
	if inv.Architecture != "x86_64" {
		arch = "arm64"
	}
	agentBin, e := r.artifact("agent-linux-" + arch)
	if e != nil {
		return res, e
	}
	coreBin, e := r.artifact("hysteria-linux-" + arch)
	if e != nil {
		return res, e
	}
	p := pathsFor(in.Node.ID)
	res.BackupPath = "/var/backups/hqip-nodes/" + in.Node.ID + "-" + time.Now().UTC().Format("20060102T150405") + "-" + secret()[:12]
	backupPayload := map[string]any{"nodeId": in.Node.ID, "backup": res.BackupPath, "candidates": pre.TakeoverCandidates, "action": "backup"}
	log("backup", "保存独立实例和待接管服务的配置、证书、账本及启停状态")
	if _, err = rm.run(ctx, pythonCommand(backupScript, backupPayload), nil); err != nil {
		return res, errors.New("创建持久备份失败，原服务未停止")
	}
	modified := false
	var addedFirewall []firewallRule
	defer func() {
		if err == nil || !modified {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if addedFirewall != nil {
			if e := cleanFirewall(rollbackCtx, rm, in.Node.ID, addedFirewall); e != nil {
				log("warning", "无法清理本次新增防火墙规则")
			}
		}
		backupPayload["action"] = "rollback"
		if _, rollbackErr := rm.run(rollbackCtx, pythonCommand(backupScript, backupPayload), nil); rollbackErr != nil {
			log("warning", "恢复旧实例失败，请使用备份 "+res.BackupPath)
			err = fmt.Errorf("%w；自动回退未完成，备份：%s", err, res.BackupPath)
		} else {
			log("rollback", "新实例已停止，原服务已恢复原配置与启停状态；备份："+res.BackupPath)
		}
	}()
	modified = true
	backupPayload["action"] = "stop"
	if _, err = rm.run(ctx, pythonCommand(backupScript, backupPayload), nil); err != nil {
		return res, errors.New("停止已确认的待接管服务失败")
	}
	if _, err = rm.run(ctx, "install -d -m 700 "+shellQuote(p.Config)+" "+shellQuote(p.State)+"; install -d -m 755 /etc/systemd/journald@hqip.conf.d", nil); err != nil {
		return res, errors.New("创建独立节点目录失败")
	}
	owner, _ := json.Marshal(instanceOwner{NodeID: in.Node.ID, ControllerID: r.controllerIdentity(), MachineID: inv.MachineID, PublicIP: in.Node.PublicIP, InstallationVersion: installationVersion})
	res.Managed = true // Pessimistic: an SSH error can arrive after the owner write completed.
	if err = rm.put(ctx, p.Config+"/owner.json", owner, "600"); err != nil {
		return
	}
	agentPath, corePath := contentPath("agent", agentBin), contentPath("hysteria", coreBin)
	log("install", "安装按内容校验且互相隔离的节点组件")
	if err = putImmutable(ctx, rm, agentPath, agentBin); err != nil {
		return
	}
	if err = putImmutable(ctx, rm, corePath, coreBin); err != nil {
		return
	}
	statsSecret := secret()
	var realityConfig *model.AgentRealityConfig
	if pre.ExistingManaged {
		var previous model.AgentConfig
		raw, _ := rm.run(ctx, "cat "+shellQuote(p.Config+"/agent.json"), nil)
		if json.Unmarshal([]byte(raw), &previous) == nil && len(previous.StatsSecret) >= 24 {
			statsSecret = previous.StatsSecret
			realityConfig = previous.Reality
		}
	}
	if realityConfig == nil {
		// Each public IP gets its own TCP endpoint. Keep shared-host HTTPS on
		// 443 intact by selecting 8443 only when the exact-IP bind is occupied.
		raw, e := rm.run(ctx, pythonCommand(realityPortScript, map[string]any{"ip": in.Node.PublicIP}), nil)
		if e != nil {
			return res, errors.New("没有可用的 Reality TCP 端口")
		}
		port, e := strconv.Atoi(strings.TrimSpace(raw))
		if e != nil || (port != 443 && port != 8443) {
			return res, errors.New("Reality 端口检查失败")
		}
		realityConfig = &model.AgentRealityConfig{Port: port, ServerName: model.DefaultRealityServerName}
	}
	// Migrate the previous automatic target without rotating keys or ports.
	// An explicitly configured different SNI remains owned by the operator.
	if realityConfig.ServerName == "" || strings.EqualFold(strings.TrimSuffix(realityConfig.ServerName, "."), "www.cloudflare.com") {
		realityConfig.ServerName = model.DefaultRealityServerName
	}
	realityConfig.BackendPort = in.Node.Port
	ac := model.AgentConfig{ControlURL: strings.TrimRight(r.options.ControlURL, "/"), NodeID: in.Node.ID, Token: in.AgentToken, Listen: fmt.Sprintf("127.0.0.1:%d", res.AuthPort), StatePath: p.State + "/state.db", StatsURL: fmt.Sprintf("http://127.0.0.1:%d", res.StatsPort), StatsSecret: statsSecret, HysteriaService: p.CoreUnit, AgentService: p.AgentUnit, NodeDir: p.Config, Managed: true, ControllerID: r.controllerIdentity(), Domain: in.Node.Domain, PublicIP: in.Node.PublicIP, CertificateMode: in.Node.CertificateMode}
	ac.Reality = realityConfig
	agentJSON, _ := json.MarshalIndent(ac, "", "  ")
	hyYAML, e := serverConfig(in.Node, res.ACMEViaCaddy, statsSecret)
	if e != nil {
		return res, e
	}
	agentUnit, coreUnit := instanceUnits(in.Node, agentPath, corePath)
	files := map[string][]byte{p.Config + "/agent.json": agentJSON, p.Config + "/hysteria.yaml": hyYAML, "/etc/systemd/system/" + p.AgentUnit: []byte(agentUnit), "/etc/systemd/system/" + p.CoreUnit: []byte(coreUnit), "/etc/systemd/journald@hqip.conf.d/limits.conf": []byte("[Journal]\nSystemMaxUse=100M\nRuntimeMaxUse=16M\nMaxRetentionSec=7day\n")}
	for file, data := range files {
		if err = rm.put(ctx, file, data, "600"); err != nil {
			return res, errors.New("写入独立节点配置失败")
		}
	}
	if in.Certificate != nil && in.Node.CertificateMode == "cloudflare" {
		if err = publishInitialCertificate(ctx, rm, in.Node, *in.Certificate); err != nil {
			return
		}
	}
	log("certificate", "启动独立 Agent 和节点，等待证书与授权同步")
	_, err = rm.run(ctx, "systemctl daemon-reload && systemctl enable "+shellQuote(p.AgentUnit)+" "+shellQuote(p.CoreUnit)+" >/dev/null && systemctl restart "+shellQuote(p.AgentUnit)+" && systemctl restart "+shellQuote(p.CoreUnit), nil)
	if err != nil {
		return res, errors.New("启动独立节点服务失败")
	}
	addedFirewall, e = applyFirewall(ctx, rm, in.Node, res.ACMEViaCaddy, realityConfig.Port)
	if e != nil {
		return res, errors.New("管理节点自有 UFW 规则失败，请检查节点防火墙")
	}
	log("verify", "验证 TLS、UDP 握手、代理 HTTPS 和该 IP 的独立出口")
	in.Node.ControlOnNode, in.Node.ACMEViaCaddy, in.Node.SharedHost = res.ControlOnNode, res.ACMEViaCaddy, res.SharedHost
	var verifyErr error
	for attempt := 0; attempt < 18; attempt++ {
		if err = wait(ctx, 5*time.Second); err != nil {
			return
		}
		verifyCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
		vr, e := r.Verify(verifyCtx, Input{Node: in.Node, ProbePassword: in.ProbePassword}, log)
		cancel()
		if e == nil {
			res.ExitIP, res.Verified, res.Message = vr.ExitIP, vr.Verified, vr.Message
			if res.ControlOnNode {
				res.Verified = false
				res.Message = "节点通过本机代理验证，等待外部验证"
			}
			for _, candidate := range pre.TakeoverCandidates {
				if candidate.Kind == "hqip-legacy" {
					// Keep the archived old owner for recovery, but do not offer this
					// disabled legacy installation when adding an unrelated second IP.
					if e := rm.put(ctx, "/etc/hqip-node/retired-by", owner, "600"); e != nil {
						log("warning", "旧安装已停止；未能写入已接管标记，后续添加 IP 可能需要再次预检")
					}
				}
			}
			return res, nil
		}
		verifyErr = e
	}
	return res, fmt.Errorf("新实例代理验证失败，已触发回退：%w", verifyErr)
}

func validateDNS(ctx context.Context, n model.Node) error {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", n.Domain)
	if err != nil {
		return errors.New("域名解析失败")
	}
	found := false
	for _, ip := range ips {
		if ip.String() != n.PublicIP {
			return errors.New("域名存在其他 IPv4 记录，每个节点域名必须只对应自身 IP")
		}
		found = true
	}
	if !found {
		return errors.New("域名 A 记录未指向节点公网 IP")
	}
	if ips, e := net.DefaultResolver.LookupIP(ctx, "ip6", n.Domain); e == nil && len(ips) > 0 {
		return errors.New("IPv4 节点域名不能含 AAAA 记录")
	}
	return nil
}

func putImmutable(ctx context.Context, rm *remote, file string, data []byte) error {
	dir := path.Dir(file)
	want := path.Base(dir)
	out, e := rm.run(ctx, "if test -e "+shellQuote(file)+"; then sha256sum "+shellQuote(file)+" | cut -d ' ' -f 1; fi", nil)
	if e != nil {
		return e
	}
	if strings.TrimSpace(out) != "" {
		if strings.TrimSpace(out) != want {
			return errors.New("共享组件目录校验不符，拒绝覆盖")
		}
		return nil
	}
	if _, e = rm.run(ctx, "install -d -m 755 "+shellQuote(dir), nil); e != nil {
		return e
	}
	return rm.put(ctx, file, data, "755")
}

func publishInitialCertificate(ctx context.Context, rm *remote, n model.Node, bundle model.CertificateBundle) error {
	p := pathsFor(n.ID)
	dir := p.State + "/certificates"
	version := "v" + strconv.FormatInt(bundle.Version, 10)
	if _, e := agent.ValidateCertificateBundle(bundle, n.Domain, time.Now()); e != nil {
		return e
	}
	if _, e := rm.run(ctx, "install -d -m 700 "+shellQuote(dir+"/"+version), nil); e != nil {
		return e
	}
	if e := rm.put(ctx, dir+"/"+version+"/cert.pem", []byte(bundle.CertificatePEM), "600"); e != nil {
		return e
	}
	if e := rm.put(ctx, dir+"/"+version+"/key.pem", []byte(bundle.PrivateKeyPEM), "600"); e != nil {
		return e
	}
	_, e := rm.run(ctx, "ln -s "+shellQuote(version)+" "+shellQuote(dir+"/current.new")+" && mv -Tf "+shellQuote(dir+"/current.new")+" "+shellQuote(dir+"/current"), nil)
	return e
}

// Package provision installs only HQIP-owned files on explicitly enrolled hosts.
package provision

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
	"gopkg.in/yaml.v3"
)

type Options struct{ ControlURL, ArtifactDir string }
type Runner struct{ options Options }
type Input struct {
	Node                                model.Node
	Password, AgentToken, ProbePassword string
}
type Result struct {
	SharedHost, Verified        bool
	ControlOnNode, ACMEViaCaddy bool
	ExitIP, Message             string
}
type LogFunc func(stage, message string)
type PinFunc func(fingerprint string) error

func New(o Options) *Runner { return &Runner{options: o} }

var domainRE = regexp.MustCompile(`^(?i)[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
var userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var idRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func Validate(n model.Node) error {
	if !idRE.MatchString(n.ID) {
		return errors.New("节点标识无效")
	}
	if n.Name == "" || len(n.Name) > 240 {
		return errors.New("节点名称不能为空且应短于 80 个汉字")
	}
	if n.SSHHost == "" || len(n.SSHHost) > 253 || strings.ContainsAny(n.SSHHost, "\r\n\t /\\") {
		return errors.New("SSH 地址无效")
	}
	if net.ParseIP(n.SSHHost) == nil && !domainRE.MatchString(n.SSHHost) {
		return errors.New("SSH 地址须为 IP 或主机名")
	}
	if ip := net.ParseIP(n.PublicIP); ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return errors.New("请填写有效的公网 IPv4")
	}
	if !domainRE.MatchString(n.Domain) || !strings.Contains(n.Domain, ".") || strings.Contains(n.Domain, "..") || net.ParseIP(n.Domain) != nil {
		return errors.New("请填写有效域名，不包含协议或路径")
	}
	if !userRE.MatchString(n.SSHUser) || n.SSHPort < 1 || n.SSHPort > 65535 || n.Port < 1 || n.Port > 65535 {
		return errors.New("SSH 用户或端口无效")
	}
	if n.BandwidthMbps != nil && (*n.BandwidthMbps <= 0 || *n.BandwidthMbps > 1e9) {
		return errors.New("带宽须为正数或无限")
	}
	if n.MonthlyQuotaBytes != nil && *n.MonthlyQuotaBytes <= 0 {
		return errors.New("流量额度须为正数或无限")
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func wait(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(duration):
		return nil
	}
}

type remote struct {
	client *ssh.Client
	sudo   bool
}

func (r *remote) run(ctx context.Context, command string, input io.Reader) (string, error) {
	s, err := r.client.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	if r.sudo {
		command = "sudo -n sh -c " + shellQuote(command)
	} else {
		command = "sh -c " + shellQuote(command)
	}
	// SSH copies stdout and stderr concurrently. A shared bytes.Buffer can lose
	// stdout even when stderr is empty because both io.Copy calls use ReadFrom.
	var out, stderr bytes.Buffer
	s.Stdout = &out
	s.Stderr = &stderr
	s.Stdin = input
	if err = s.Start(command); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case <-ctx.Done():
		_ = s.Close()
		return "", ctx.Err()
	case err = <-done:
	}
	if err != nil {
		return out.String() + stderr.String(), err
	}
	return out.String(), nil
}
func (r *remote) put(ctx context.Context, path string, data []byte, mode string) error {
	hash := sha256.Sum256(data)
	cmd := "umask 077; cat > " + shellQuote(path+".new") + " && test \"$(sha256sum " + shellQuote(path+".new") + " | cut -d ' ' -f 1)\" = " + shellQuote(hex.EncodeToString(hash[:])) + " && chmod " + mode + " " + shellQuote(path+".new") + " && mv -f " + shellQuote(path+".new") + " " + shellQuote(path)
	_, err := r.run(ctx, cmd, bytes.NewReader(data))
	return err
}

func (r *Runner) connect(ctx context.Context, in Input, pin PinFunc) (*remote, error) {
	cfg := &ssh.ClientConfig{User: in.Node.SSHUser, Auth: []ssh.AuthMethod{ssh.Password(in.Password)}, Timeout: 20 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			if in.Node.HostKey != "" && in.Node.HostKey != fp {
				return errors.New("SSH 主机指纹已改变，已停止连接")
			}
			if pin == nil {
				return errors.New("缺少 SSH 指纹持久化回调")
			}
			return pin(fp)
		},
	}
	address := net.JoinHostPort(in.Node.SSHHost, strconv.Itoa(in.Node.SSHPort))
	conn, err := (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("SSH 网络连接失败: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, ch, req, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		conn.Close()
		return nil, errors.New("SSH 握手、密码或主机指纹验证失败")
	}
	_ = conn.SetDeadline(time.Time{})
	rm := &remote{client: ssh.NewClient(c, ch, req), sudo: in.Node.SSHUser != "root"}
	return rm, nil
}

func (r *Runner) artifact(name string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(r.options.ArtifactDir, "manifest.json"))
	if err != nil {
		return nil, errors.New("缺少发布清单，请先运行构建脚本")
	}
	var manifest struct {
		SHA256 map[string]string `json:"sha256"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	want := manifest.SHA256[name]
	if len(want) != 64 {
		return nil, fmt.Errorf("发布清单缺少 %s", name)
	}
	data, err := os.ReadFile(filepath.Join(r.options.ArtifactDir, name))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s 校验失败", name)
	}
	return data, nil
}

func (r *Runner) Install(ctx context.Context, in Input, log LogFunc, pin PinFunc) (res Result, err error) {
	if err = Validate(in.Node); err != nil {
		return
	}
	if log == nil {
		log = func(string, string) {}
	}
	base, parseErr := url.Parse(r.options.ControlURL)
	if parseErr != nil || base.Scheme != "https" || base.Host == "" {
		return res, errors.New("控制台公网地址必须是 HTTPS")
	}
	log("preflight", "检查 SSH、系统与域名")
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", in.Node.Domain)
	if err != nil {
		return res, errors.New("域名解析失败")
	}
	found := false
	for _, ip := range ips {
		if ip.String() == in.Node.PublicIP {
			found = true
		} else {
			return res, errors.New("域名存在指向其他服务器的 A 记录，请修正后重试")
		}
	}
	if !found {
		return res, errors.New("域名 A 记录未指向填写的公网 IP")
	}
	if ipv6, lookupErr := net.DefaultResolver.LookupIP(ctx, "ip6", in.Node.Domain); lookupErr == nil && len(ipv6) > 0 {
		return res, errors.New("第一版节点使用 IPv4，请移除该域名的 AAAA 记录后重试，以免证书验证走向未配置的 IPv6")
	}
	rm, err := r.connect(ctx, in, pin)
	if err != nil {
		return res, err
	}
	defer rm.client.Close()
	out, err := rm.run(ctx, `set -eu
test "$(id -u)" = 0
. /etc/os-release
printf '%s\n%s\n' "$ID" "$VERSION_ID"
uname -m
printf '%s\n' "$(cat /etc/machine-id)"
df -Pk / | tail -1 | awk '{print $4}'
command -v systemctl >/dev/null
command -v ss >/dev/null
`, nil)
	if err != nil {
		return res, errors.New("需要 root 或免密 sudo、systemd 和 ss 工具")
	}
	lines := strings.Fields(out)
	if len(lines) < 5 {
		return res, errors.New("无法识别服务器环境")
	}
	supported := (lines[0] == "ubuntu" && (lines[1] == "22.04" || lines[1] == "24.04")) || (lines[0] == "debian" && lines[1] == "12")
	if !supported {
		return res, errors.New("仅支持 Ubuntu 22.04/24.04 或 Debian 12")
	}
	arch := ""
	switch lines[2] {
	case "x86_64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		return res, errors.New("仅支持 amd64/arm64")
	}
	freeKB, _ := strconv.ParseInt(lines[4], 10, 64)
	if freeKB < 256*1024 {
		return res, errors.New("可用磁盘不足 256 MiB")
	}
	localID, _ := os.ReadFile("/etc/machine-id")
	res.ControlOnNode, res.ACMEViaCaddy = deploymentTopology(in.Node, string(localID), lines[3], runtime.GOOS)
	res.SharedHost = res.ACMEViaCaddy
	if res.ControlOnNode && base.Hostname() != in.Node.Domain {
		return res, errors.New("同机节点请使用控制台域名，以保证 ACME 验证路由正确")
	}
	check := fmt.Sprintf(`set -eu
if test -f /etc/hqip-node/owner; then
 test "$(cat /etc/hqip-node/owner)" = %s || { echo owner-conflict; exit 1; }
else
 test -z "$(ss -H -lun 'sport = :%d')" || { echo udp-port-conflict; exit 1; }
 for p in 18081 18082; do test -z "$(ss -H -ltn "sport = :$p")" || { echo local-port-conflict; exit 1; }; done
fi
`, shellQuote(in.Node.ID), in.Node.Port)
	if !res.ACMEViaCaddy {
		check += `test -z "$(ss -H -ltn 'sport = :80')" || test -f /etc/hqip-node/owner || { echo http-port-conflict; exit 1; }
`
	} else {
		check += `test -f /etc/hqip/Caddyfile && grep -Fq '127.0.0.1:18080' /etc/hqip/Caddyfile && grep -Fq '/.well-known/acme-challenge/' /etc/hqip/Caddyfile || { echo missing-shared-caddy-route; exit 1; }
`
	}
	out, err = rm.run(ctx, check, nil)
	if err != nil {
		return res, fmt.Errorf("端口或部署所有权检查失败（%s）", strings.TrimSpace(out))
	}
	agentBin, err := r.artifact("agent-linux-" + arch)
	if err != nil {
		return res, err
	}
	hyBin, err := r.artifact("hysteria-linux-" + arch)
	if err != nil {
		return res, err
	}
	log("install", "检查通过，安装已校验的节点组件")
	_, err = rm.run(ctx, "install -d -m 755 /opt/hqip-node; install -d -m 700 /etc/hqip-node /var/lib/hqip-agent /var/lib/hqip-hysteria; install -d -m 755 /etc/systemd/journald@hqip.conf.d", nil)
	if err != nil {
		return res, errors.New("无法创建节点目录")
	}
	if err = rm.put(ctx, "/etc/hqip-node/owner", []byte(in.Node.ID), "600"); err != nil {
		return res, err
	}
	if err = rm.put(ctx, "/opt/hqip-node/agent", agentBin, "755"); err != nil {
		return res, errors.New("传输 Agent 失败")
	}
	if err = rm.put(ctx, "/opt/hqip-node/hysteria", hyBin, "755"); err != nil {
		return res, errors.New("传输 Hysteria 失败")
	}
	// Retain the original stats secret across retries so the last running config remains valid.
	existing, _ := rm.run(ctx, "cat /etc/hqip-node/agent.json 2>/dev/null", nil)
	var old model.AgentConfig
	_ = json.Unmarshal([]byte(existing), &old)
	statsSecret := old.StatsSecret
	if statsSecret == "" {
		statsSecret = secret()
	}
	ac := model.AgentConfig{ControlURL: strings.TrimRight(r.options.ControlURL, "/"), NodeID: in.Node.ID, Token: in.AgentToken, Listen: "127.0.0.1:18081", StatePath: "/var/lib/hqip-agent/state.db", StatsURL: "http://127.0.0.1:18082", StatsSecret: statsSecret, HysteriaService: "hqip-hysteria.service"}
	agentJSON, _ := json.MarshalIndent(ac, "", "  ")
	hyYAML, err := serverConfig(in.Node, res.ACMEViaCaddy, statsSecret)
	if err != nil {
		return res, err
	}
	files := map[string][]byte{
		"/etc/hqip-node/agent.json":                     agentJSON,
		"/etc/hqip-node/hysteria.yaml":                  hyYAML,
		"/etc/systemd/system/hqip-agent.service":        []byte(agentUnit),
		"/etc/systemd/system/hqip-hysteria.service":     []byte(hysteriaUnit),
		"/etc/systemd/journald@hqip.conf.d/limits.conf": []byte("[Journal]\nSystemMaxUse=100M\nRuntimeMaxUse=16M\nMaxRetentionSec=7day\n"),
	}
	for path, data := range files {
		if err = rm.put(ctx, path, data, "600"); err != nil {
			return res, errors.New("写入节点配置失败")
		}
	}
	log("certificate", "启动 Agent，自动申请并续期 TLS 证书")
	_, err = rm.run(ctx, `set -eu
systemctl daemon-reload
systemctl enable hqip-agent.service hqip-hysteria.service >/dev/null
systemctl restart hqip-agent.service
systemctl restart hqip-hysteria.service
`, nil)
	if err != nil {
		return res, errors.New("启动节点服务失败，请检查节点日志")
	}
	// Add only explicit required firewall allowances, never reset firewall policy.
	fw := fmt.Sprintf(`if command -v ufw >/dev/null 2>&1 && ufw status | head -1 | grep -q '^Status: active$'; then ufw allow %d/udp >/dev/null; ufw allow 80/tcp >/dev/null; fi`, in.Node.Port)
	if _, e := rm.run(ctx, fw, nil); e != nil {
		log("warning", "未能自动放行 UFW，请检查 UDP 节点端口及 TCP 80")
	}
	log("verify", "等待证书签发和节点鉴权同步，然后验证实际代理连接")
	var lastErr error
	in.Node.ControlOnNode, in.Node.ACMEViaCaddy, in.Node.SharedHost = res.ControlOnNode, res.ACMEViaCaddy, res.SharedHost
	for attempt := 0; attempt < 18; attempt++ {
		if err = wait(ctx, 5*time.Second); err != nil {
			return res, err
		}
		verifyCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
		vr, e := r.Verify(verifyCtx, Input{Node: in.Node, ProbePassword: in.ProbePassword}, log)
		cancel()
		if e == nil {
			vr.SharedHost, vr.ControlOnNode, vr.ACMEViaCaddy = res.SharedHost, res.ControlOnNode, res.ACMEViaCaddy
			if res.ControlOnNode {
				vr.Verified = false
				vr.Message = "节点已安装并通过本机代理验证，等待外部 Mihomo 验证"
			}
			return vr, nil
		}
		lastErr = e
	}
	return res, fmt.Errorf("组件已安装，但代理验证失败：%w", lastErr)
}

func serverConfig(n model.Node, acmeViaCaddy bool, statsSecret string) ([]byte, error) {
	listenHost := "0.0.0.0"
	port := 80
	if acmeViaCaddy {
		listenHost = "127.0.0.1"
		port = 18080
	}
	rules := []string{"reject(0.0.0.0/8)", "reject(10.0.0.0/8)", "reject(100.64.0.0/10)", "reject(127.0.0.0/8)", "reject(169.254.0.0/16)", "reject(172.16.0.0/12)", "reject(192.168.0.0/16)", "reject(224.0.0.0/4)", "reject(240.0.0.0/4)", "reject(::/128)", "reject(::1/128)", "reject(fc00::/7)", "reject(fe80::/10)", "reject(ff00::/8)"}
	return yaml.Marshal(map[string]any{
		"listen":       fmt.Sprintf(":%d", n.Port),
		"acme":         map[string]any{"domains": []string{n.Domain}, "ca": "letsencrypt", "dir": "/var/lib/hqip-hysteria/acme", "type": "http", "listenHost": listenHost, "http": map[string]any{"altPort": port}},
		"auth":         map[string]any{"type": "http", "http": map[string]any{"url": "http://127.0.0.1:18081/auth?secret=" + statsSecret}},
		"trafficStats": map[string]any{"listen": "127.0.0.1:18082", "secret": statsSecret},
		"acl":          map[string]any{"inline": rules},
		"resolver":     map[string]any{"type": "udp", "udp": map[string]any{"addr": "1.1.1.1:53", "timeout": "4s"}},
		"outbounds":    []any{map[string]any{"name": "direct", "type": "direct", "direct": map[string]any{"mode": "4"}}},
	})
}

func (r *Runner) Verify(ctx context.Context, in Input, log LogFunc) (Result, error) {
	controlOnNode := in.Node.ControlOnNode && runtime.GOOS == "linux"
	res := Result{SharedHost: in.Node.SharedHost, ControlOnNode: controlOnNode, ACMEViaCaddy: in.Node.ACMEViaCaddy || in.Node.SharedHost}
	if in.ProbePassword == "" {
		return res, errors.New("缺少专用探测凭据")
	}
	artifactName, err := probeArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return res, err
	}
	binary, err := r.artifact(artifactName)
	if err != nil {
		return res, err
	}
	dir, err := os.MkdirTemp("", "hqip-probe-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(dir)
	binPath := filepath.Join(dir, artifactName)
	if err = os.WriteFile(binPath, binary, 0700); err != nil {
		return res, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return res, err
	}
	addr := listener.Addr().String()
	listener.Close()
	config, _ := yaml.Marshal(map[string]any{"server": net.JoinHostPort(in.Node.PublicIP, strconv.Itoa(in.Node.Port)), "auth": in.ProbePassword, "tls": map[string]any{"sni": in.Node.Domain, "insecure": false}, "socks5": map[string]any{"listen": addr}})
	configPath := filepath.Join(dir, "client.yaml")
	if err = os.WriteFile(configPath, config, 0600); err != nil {
		return res, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, binPath, "client", "--config", configPath, "--log-level", "error")
	hideProbeCommand(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return res, err
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	dialer, err := proxy.SOCKS5("tcp", addr, nil, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		return res, err
	}
	transport := &http.Transport{TLSHandshakeTimeout: 8 * time.Second, DialContext: func(c context.Context, network, address string) (net.Conn, error) {
		if d, ok := dialer.(proxy.ContextDialer); ok {
			return d.DialContext(c, network, address)
		}
		return nil, errors.New("SOCKS context dialer unavailable")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second}
	var lastErr error
	for i := 0; i < 4; i++ {
		if err = wait(ctx, time.Second); err != nil {
			return res, err
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
		resp, e := client.Do(req)
		if e != nil {
			lastErr = e
			continue
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, 128))
		resp.Body.Close()
		if e != nil {
			lastErr = e
			continue
		}
		ip := strings.TrimSpace(string(b))
		if resp.StatusCode != 200 || net.ParseIP(ip) == nil {
			lastErr = errors.New("出口探测返回无效响应")
			continue
		}
		if ip != in.Node.PublicIP {
			return res, fmt.Errorf("出口 IP %s 与登记公网 IP 不一致", ip)
		}
		res.ExitIP = ip
		res.Verified = !res.ControlOnNode
		res.Message = "TLS、UDP 代理连接与出口 IP 已验证"
		return res, nil
	}
	if lastErr == nil {
		lastErr = errors.New("代理连接超时")
	}
	return res, fmt.Errorf("无法通过节点访问 HTTPS 出口探测服务: %w", lastErr)
}

const agentUnit = `[Unit]
Description=HQIP node agent
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStart=/opt/hqip-node/agent --config /etc/hqip-node/agent.json
Restart=always
RestartSec=3
UMask=0077
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/hqip-agent
ProtectHome=true
PrivateTmp=true
LimitNOFILE=65536
LogNamespace=hqip
[Install]
WantedBy=multi-user.target
`
const hysteriaUnit = `[Unit]
Description=HQIP Hysteria 2 node
After=network-online.target hqip-agent.service
Wants=network-online.target hqip-agent.service
[Service]
Type=simple
WorkingDirectory=/var/lib/hqip-hysteria
ExecStart=/opt/hqip-node/hysteria server --config /etc/hqip-node/hysteria.yaml --log-level warn
Restart=always
RestartSec=5
UMask=0077
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/hqip-hysteria
ProtectHome=true
PrivateTmp=true
LimitNOFILE=65536
LogNamespace=hqip
[Install]
WantedBy=multi-user.target
`

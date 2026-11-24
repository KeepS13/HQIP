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

type Options struct{ ControlURL, ArtifactDir, ControllerID string }
type Runner struct{ options Options }
type Input struct {
	Node                                model.Node
	Password, AgentToken, ProbePassword string
	Takeover                            bool
	PreflightFingerprint                string
	Certificate                         *model.CertificateBundle
}
type Result struct {
	SharedHost, Verified                     bool
	ControlOnNode, ACMEViaCaddy              bool
	ExitIP, Message                          string
	MachineID, BackupPath                    string
	AuthPort, StatsPort, InstallationVersion int
	Managed                                  bool
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
	authPort, statsPort := localPorts(n)
	if authPort < 1024 || statsPort < 1024 || authPort > 65535 || statsPort > 65535 || authPort == statsPort {
		return errors.New("节点本地鉴权和统计端口无效")
	}
	if n.CertificateMode != "" && n.CertificateMode != "http" && n.CertificateMode != "cloudflare" {
		return errors.New("不支持的证书管理模式")
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
	var identityErr error
	cfg := &ssh.ClientConfig{User: in.Node.SSHUser, Auth: []ssh.AuthMethod{ssh.Password(in.Password)}, Timeout: 20 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			if in.Node.HostKey != "" && in.Node.HostKey != fp {
				identityErr = ErrSSHIdentity
				return identityErr
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
	deadline := time.Now().Add(30 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = conn.SetDeadline(deadline)
	c, ch, req, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		conn.Close()
		if identityErr != nil {
			return nil, identityErr
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, ErrSSHAuthentication
		}
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

func (r *Runner) Install(ctx context.Context, in Input, log LogFunc, pin PinFunc) (Result, error) {
	return r.installInstance(ctx, in, log, pin)
}

func serverConfig(n model.Node, acmeViaCaddy bool, statsSecret string) ([]byte, error) {
	return instanceConfig(n, acmeViaCaddy, statsSecret)
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

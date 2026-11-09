package client

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
	"strconv"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

// Platform owns machine changes; the engine never changes the system proxy
// during candidate validation.
type Platform interface {
	SecureDir(string) error
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
	Prepare(*exec.Cmd)
	Attach(*exec.Cmd) (io.Closer, error)
	EnableProxy(int) error
	RestoreProxy() error
	StartTUN(context.Context, []byte) (func() error, error)
}
type Engine interface {
	Apply(context.Context, Profile, rules.Bundle, string, string, bool, string) error
	Stop() error
	Alive() bool
	VerifyCurrent(context.Context, string) error
	SetSuspended(context.Context, bool) error
}
type Core struct {
	mu                       sync.Mutex
	Path, Dir                string
	Platform                 Platform
	process                  *exec.Cmd
	done                     chan struct{}
	job                      io.Closer
	cancel                   context.CancelFunc
	tunStop                  func() error
	port, apiPort, probePort int
	secret                   string
	lastProfile              Profile
	controlURL               string
}

func portString(p int) string { return strconv.Itoa(p) }
func freePort() (int, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	p := l.Addr().(*net.TCPAddr).Port
	e = l.Close()
	return p, e
}
func randomID() string { b := make([]byte, 24); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func (c *Core) verifyBinary() error {
	b, e := os.ReadFile(c.Path)
	if e != nil {
		return errors.New("未找到配套 sing-box 内核")
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != CoreSHA256 {
		return errors.New("sing-box 校验失败，拒绝运行修改过的内核")
	}
	return nil
}
func (c *Core) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Path, args...)
	c.Platform.Prepare(cmd)
	return cmd
}
func (c *Core) Check(ctx context.Context, b []byte, path string) error {
	if e := os.WriteFile(path, b, 0600); e != nil {
		return e
	}
	cmd := c.command(ctx, "check", "-c", path)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if e := cmd.Run(); e != nil {
		return errors.New("内核配置校验失败，请检查规则与节点参数")
	}
	return nil
}
func (c *Core) Apply(ctx context.Context, p Profile, b rules.Bundle, selected, mode string, tun bool, server string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.verifyBinary(); e != nil {
		return e
	}
	if e := c.Platform.SecureDir(c.Dir); e != nil {
		return e
	}
	geo := ""
	if len(b.GeoIPCN.CIDRs) > 0 {
		source, e := b.GeoIPSource()
		if e != nil {
			return e
		}
		src := filepath.Join(c.Dir, "geoip-cn.json")
		geo = filepath.Join(c.Dir, "geoip-cn.srs")
		if e = os.WriteFile(src, source, 0600); e != nil {
			return e
		}
		cmd := c.command(ctx, "rule-set", "compile", "--output", geo, src)
		if e = cmd.Run(); e != nil {
			return errors.New("中国 IP 规则编译失败")
		}
	}
	port, e := freePort()
	if e != nil {
		return e
	}
	apiPort, e := freePort()
	if e != nil {
		return e
	}
	secret := randomID()
	probePort, e := freePort()
	if e != nil {
		return e
	}
	expectedIP := ""
	for _, n := range p.Nodes {
		if n.ID == selected {
			expectedIP = n.PublicIP
		}
	}
	opts := ConfigOptions{Profile: p, Bundle: b, Selected: selected, Mode: mode, TUN: tun, MixedPort: port, ProbePort: probePort, APIPort: apiPort, APISecret: secret, GeoPath: geo, ServerURL: server}
	// Probe the actual process; a separate probe process consumes another slot.
	data, e := GenerateConfig(opts)
	if e != nil {
		return e
	}
	if e = c.stopLocked(); e != nil {
		return e
	}
	path := filepath.Join(c.Dir, "active.json")
	if e = c.Check(ctx, data, path); e != nil {
		return e
	}
	if tun {
		stop, e := c.Platform.StartTUN(ctx, data)
		if e != nil {
			return e
		}
		c.tunStop = stop
		c.port = port
		c.probePort = probePort
		c.apiPort = apiPort
		c.secret = secret
		if e = Probe(ctx, probePort, expectedIP); e != nil {
			_ = c.stopLocked()
			return e
		}
		c.lastProfile = p
		c.controlURL = server
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.process = c.command(runCtx, "run", "-c", path)
	if e = c.process.Start(); e != nil {
		cancel()
		c.process = nil
		return errors.New("内核启动失败")
	}
	c.job, e = c.Platform.Attach(c.process)
	if e != nil {
		_ = c.process.Process.Kill()
		_ = c.process.Wait()
		c.process = nil
		cancel()
		return e
	}
	c.done = make(chan struct{})
	proc := c.process
	done := c.done
	go func() { _ = proc.Wait(); close(done) }()
	c.port = port
	c.probePort = probePort
	c.apiPort = apiPort
	c.secret = secret
	if e = Probe(ctx, probePort, expectedIP); e != nil {
		_ = c.stopLocked()
		return e
	}
	if e = ctx.Err(); e != nil {
		_ = c.stopLocked()
		return e
	}
	if e = c.Platform.EnableProxy(port); e != nil {
		_ = c.stopLocked()
		return e
	}
	c.lastProfile = p
	c.controlURL = server
	return nil
}
func (c *Core) Stop() error { c.mu.Lock(); defer c.mu.Unlock(); return c.stopLocked() }
func (c *Core) stopLocked() error {
	err := c.Platform.RestoreProxy()
	if c.tunStop != nil {
		if e := c.tunStop(); err == nil {
			err = e
		}
		c.tunStop = nil
	}
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if c.job != nil {
		_ = c.job.Close()
		c.job = nil
	}
	if c.done != nil {
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			if c.process != nil {
				_ = c.process.Process.Kill()
			}
		}
		c.done = nil
	}
	c.process = nil
	c.port = 0
	_ = os.Remove(filepath.Join(c.Dir, "active.json"))
	_ = os.Remove(filepath.Join(c.Dir, "candidate.json"))
	return err
}
func (c *Core) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tunStop != nil {
		conn, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", portString(c.port)), time.Second)
		if e == nil {
			conn.Close()
		}
		return e == nil
	}
	if c.done == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func Probe(ctx context.Context, port int, expectedIP ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	u, _ := url.Parse("http://127.0.0.1:" + portString(port))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(u)
	transport.TLSHandshakeTimeout = 8 * time.Second
	defer transport.CloseIdleConnections()
	h := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ready := false
	for i := 0; i < 30; i++ {
		conn, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", u.Host)
		if e == nil {
			conn.Close()
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		return errors.New("本地代理端口未就绪，已停止代理")
	}
	expected := ""
	if len(expectedIP) > 0 {
		expected = expectedIP[0]
	}
	return probeExit(ctx, h, expected, exitProbeTargets)
}
func (c *Core) Select(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.port == 0 {
		return errors.New("内核未连接")
	}
	body, _ := json.Marshal(map[string]string{"name": "node-" + id})
	req, _ := http.NewRequestWithContext(ctx, "PUT", fmt.Sprintf("http://127.0.0.1:%d/proxies/proxy", c.apiPort), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	h := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	res, e := h.Do(req)
	if e != nil {
		return e
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		return errors.New("节点切换失败")
	}
	expectedIP := ""
	for _, n := range c.lastProfile.Nodes {
		if n.ID == id {
			expectedIP = n.PublicIP
			break
		}
	}
	return Probe(ctx, c.probePort, expectedIP)
}

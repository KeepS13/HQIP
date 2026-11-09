package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const suspendedClashMode = "hqip-paused"

func (c *Core) VerifyCurrent(ctx context.Context, expected string) error {
	c.mu.Lock()
	port, probe, secret := c.port, c.probePort, c.secret
	c.mu.Unlock()
	if port == 0 {
		return errors.New("本地代理内核未运行")
	}
	// HY2 TLS errors are otherwise flattened into an HTTP CONNECT failure by
	// the local proxy. Observe only the certificate category, never persist logs.
	finish, err := c.watchProbeCertificate(ctx)
	if err != nil {
		return err
	}
	err = Probe(ctx, probe, expected)
	if certificate := finish(err != nil); certificate {
		err = errors.New("节点 HTTPS 证书校验失败，已停止代理")
	}
	c.mu.Lock()
	current := c.port == port && c.secret == secret
	c.mu.Unlock()
	if !current {
		return context.Canceled
	}
	return err
}

// The listener/TUN stays installed while business traffic is paused. Only the
// explicit probe inlet and existing management bypasses remain usable.
func (c *Core) SetSuspended(ctx context.Context, suspended bool) error {
	port, secret, err := c.metricsEndpoint()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	mode := "Rule"
	if suspended {
		mode = suspendedClashMode
	}
	data, _ := json.Marshal(map[string]string{"mode": mode})
	if err = coreControl(ctx, port, secret, "PATCH", "/configs", data); err != nil {
		return err
	}
	var config struct {
		Mode string `json:"mode"`
	}
	if err = readCoreJSON(ctx, port, secret, "/configs", &config); err != nil {
		return err
	}
	if config.Mode != mode {
		return errors.New("内核未应用转发暂停状态")
	}
	if suspended {
		c.mu.Lock()
		profile, controlURL := c.lastProfile, c.controlURL
		c.mu.Unlock()
		if err = closeForwardedConnections(ctx, port, secret, profile, controlURL); err != nil {
			return err
		}
	}
	nextPort, nextSecret, err := c.metricsEndpoint()
	if err != nil || nextPort != port || nextSecret != secret {
		return context.Canceled
	}
	return nil
}

func closeForwardedConnections(ctx context.Context, port int, secret string, profile Profile, controlURL string) error {
	var state struct {
		Connections []struct {
			ID       string   `json:"id"`
			Chains   []string `json:"chains"`
			Metadata struct {
				Type          string `json:"type"`
				Host          string `json:"host"`
				DestinationIP string `json:"destinationIP"`
				ProcessPath   string `json:"processPath"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := readCoreJSON(ctx, port, secret, "/connections", &state); err != nil {
		return err
	}
	for _, conn := range state.Connections {
		if strings.HasSuffix(conn.Metadata.Type, "/probe-in") {
			continue
		}
		proxied := false
		for _, chain := range conn.Chains {
			if chain == "proxy" || strings.HasPrefix(chain, "node-") {
				proxied = true
			}
		}
		if !proxied && managementConnection(conn.Metadata.Host, conn.Metadata.DestinationIP, conn.Metadata.ProcessPath, profile, controlURL) {
			continue
		}
		if conn.ID == "" {
			return errors.New("内核业务连接标识无效")
		}
		if err := coreControl(ctx, port, secret, "DELETE", "/connections/"+url.PathEscape(conn.ID), nil); err != nil {
			return err
		}
	}
	return nil
}

func managementConnection(host, ip, process string, profile Profile, controlURL string) bool {
	if process != "" {
		if i := strings.LastIndex(process, " ("); i >= 0 && strings.HasSuffix(process, ")") {
			process = process[:i]
		}
		name := strings.ToLower(filepath.Base(strings.ReplaceAll(process, "\\", "/")))
		switch name {
		case "hqip.exe", "hqip-windows-amd64.exe", "hqip-helper.exe", "sing-box.exe":
			return true
		}
	}
	var domains []string
	if migrateOfficialServerURL(controlURL) == DefaultServerURL {
		domains = officialBypassDomains()
	} else if u, err := url.Parse(controlURL); err == nil && u.Hostname() != "" {
		domains = append(domains, u.Hostname())
	}
	for _, node := range profile.Nodes {
		domains = append(domains, node.Server)
	}
	for _, domain := range domains {
		if domain != "" && (strings.EqualFold(host, domain) || ip == domain) {
			return true
		}
	}
	return false
}

func coreControl(ctx context.Context, port int, secret, method, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	h := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := h.Do(req)
	if err != nil {
		return errors.New("本地代理控制接口不可用")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("本地代理控制请求失败")
	}
	return nil
}

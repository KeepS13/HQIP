package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

type exitProbeTarget struct {
	name, url string
	trace     bool
}

// Both requests use the same probe-in listener and selected HY2 outbound. A
// failing measurement provider must not be confused with a failing VPN node.
var exitProbeTargets = []exitProbeTarget{
	{"Cloudflare", "https://www.cloudflare.com/cdn-cgi/trace", true},
	{"IPify", "https://api.ipify.org", false},
}

var errExitMismatch = errors.New("出口 IP 与目标节点不一致，已停止代理")

func probeExit(ctx context.Context, h *http.Client, expected string, targets []exitProbeTarget) error {
	want := net.ParseIP(expected)
	if expected != "" && want == nil {
		return errors.New("目标节点公网 IP 无效")
	}
	var failures []string
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		ip, err := readExitIP(ctx, h, target)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			// Never conceal an unexpected exit by trying another provider.
			if want != nil && !ip.Equal(want) {
				return errExitMismatch
			}
			return nil
		}
		failures = append(failures, target.name+"："+probeFailureReason(err))
	}
	return fmt.Errorf("节点 HTTPS 出口验证失败（%s），已停止代理；不会回退旧节点", strings.Join(failures, "；"))
}

func readExitIP(ctx context.Context, h *http.Client, target exitProbeTarget) (net.IP, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.url, nil)
	if err != nil {
		return nil, errors.New("验证地址无效")
	}
	resp, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("验证服务返回 HTTP %d", resp.StatusCode)
	}
	const limit = 32 << 10
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("验证响应过大")
	}
	found := strings.TrimSpace(string(data))
	if target.trace {
		found = ""
		count := 0
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "ip=") {
				found = strings.TrimSpace(strings.TrimPrefix(line, "ip="))
				count++
			}
		}
		if count != 1 {
			return nil, errors.New("验证响应缺少唯一出口 IP")
		}
	}
	ip := net.ParseIP(found)
	if ip == nil {
		return nil, errors.New("验证响应未包含有效出口 IP")
	}
	return ip, nil
}

func probeFailureReason(err error) string {
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "HTTPS 证书校验失败"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return "HTTPS 验证超时"
	}
	// Keep network errors and URLs out of user-visible reports. Parser/status
	// errors above contain fixed text and no credentials or response bodies.
	if strings.HasPrefix(err.Error(), "验证") {
		return err.Error()
	}
	return "无法通过节点访问 HTTPS 验证服务"
}

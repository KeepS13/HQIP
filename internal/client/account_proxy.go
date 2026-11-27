package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var previewAccountProxyFallback atomic.Bool

// SetPreviewAccountProxyFallback opts the preview Windows host into using an
// existing, explicit third-party loopback proxy when direct TLS cannot connect.
// Legacy hosts and Android retain their protected/direct transport policy.
func SetPreviewAccountProxyFallback(enabled bool) {
	previewAccountProxyFallback.Store(enabled)
}

type accountTLSDial func(context.Context) (net.Conn, error)

// Route selection finishes before HTTP is sent. In particular, an OAuth code
// exchange or token rotation is never replayed through a different transport.
func dialAccountTLS(ctx context.Context, direct, fallback accountTLSDial, budget time.Duration) (net.Conn, error) {
	if fallback == nil {
		return direct(ctx)
	}
	directCtx, cancel := context.WithTimeout(ctx, budget)
	conn, err := direct(directCtx)
	cancel()
	if err == nil {
		return conn, nil
	}
	if conn != nil {
		conn.Close()
	}
	var certificate *tls.CertificateVerificationError
	if ctx.Err() != nil || errors.As(err, &certificate) {
		return nil, err
	}
	conn, fallbackErr := fallback(ctx)
	if fallbackErr != nil {
		return nil, errors.Join(err, fallbackErr)
	}
	return conn, nil
}

// Only a manually enabled HTTP proxy on loopback is eligible. PAC scripts,
// environment proxies, remote proxies and proxy credentials are not inferred.
func previewLoopbackProxy(server, bypass, host string) string {
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() || host == "localhost" {
		return ""
	}
	for _, pattern := range strings.Split(strings.ToLower(bypass), ";") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "<local>" && !strings.Contains(host, ".") {
			return ""
		}
		if ok, _ := path.Match(pattern, host); ok {
			return ""
		}
	}
	server = strings.TrimSpace(server)
	if strings.Contains(server, "=") {
		selected := ""
		for _, item := range strings.Split(server, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "https") {
				selected = strings.TrimSpace(value)
			}
		}
		server = selected
	}
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	proxyHost := u.Hostname()
	if strings.EqualFold(proxyHost, "localhost") {
		proxyHost = "127.0.0.1"
	}
	ip := net.ParseIP(proxyHost)
	port, err := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return ""
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port))
}

// CONNECT carries only the destination authority. The account request and its
// credentials remain inside the same verified end-to-end TLS connection.
func dialAccountProxyTLS(ctx context.Context, proxy, target string, config *tls.Config) (_ net.Conn, err error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer func() {
		stop()
		if err != nil {
			conn.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err = conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if err = request.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(conn, 16<<10))
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK || reader.Buffered() != 0 {
		return nil, errors.New("本机代理未建立账户服务安全连接")
	}
	secured := tls.Client(conn, config)
	if err = secured.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return secured, nil
}

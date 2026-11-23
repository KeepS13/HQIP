package client

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// accountRequest retries at most once, within the caller's total budget. Only
// reads may be replayed. Rotation keeps its persisted requestId and is recovered
// by the next sync (including after restart), rather than nested HTTP retries.
// An OAuth code exchange or selection may have committed when its reply is lost.
func accountRequest(h *http.Client, req *http.Request) (*http.Response, error) {
	canReplay := req.Method == http.MethodGet
	response, err := h.Do(req)
	if err == nil || !canReplay || req.Context().Err() != nil || !transientTransport(err) {
		return response, err
	}
	if response != nil {
		response.Body.Close()
	}
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-timer.C:
	}
	next := req.Clone(req.Context())
	if req.GetBody != nil {
		next.Body, err = req.GetBody()
		if err != nil {
			return nil, err
		}
	}
	h.CloseIdleConnections()
	return h.Do(next)
}

func transientTransport(err error) bool {
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) || errors.Is(err, context.Canceled) {
		return false
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// Keep diagnostics actionable without exposing credential-bearing URLs or raw
// transport errors in View. A network failure never becomes ErrUnauthorized.
func accountTransportError(err error) error {
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return errors.New("账户服务 HTTPS 证书校验失败")
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return temporary(errors.New("账户服务域名解析失败，请检查当前网络 DNS；登录状态已保留"))
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return temporary(errors.New("账户服务连接超时，将自动重试；登录状态已保留"))
	}
	return temporary(errors.New("账户服务暂时无法直连，将自动重试；登录状态已保留"))
}

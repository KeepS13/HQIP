//go:build windows

package client

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestPreviewProxyRejectsHQIPAndOwnListeners(t *testing.T) {
	for _, name := range []string{`D:\software\HQIP Preview\versions\bundled\hqip-runtime.exe`, `C:\HQIP\sing-box.exe`, `C:\HQIP.exe`, `C:\hqip-windows-amd64.exe`} {
		if externalProxyExecutable(name) {
			t.Errorf("accepted own proxy executable %q", name)
		}
	}
	if !externalProxyExecutable(`C:\Clash\mihomo.exe`) {
		t.Error("rejected third-party proxy executable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if externalProxyListener(listener.Addr().String()) {
		t.Fatal("accepted the current HQIP process as fallback")
	}
}

// Explicit, read-only live acceptance; ordinary tests do not contact production.
func TestPreviewAccountNetworkLive(t *testing.T) {
	if os.Getenv("HQIP_LIVE_ACCOUNT_NETWORK") != "1" {
		t.Skip("set HQIP_LIVE_ACCOUNT_NETWORK=1 for the configured Windows network")
	}
	SetPreviewAccountProxyFallback(true)
	defer SetPreviewAccountProxyFallback(false)
	transport := newDirectHTTPTransport()
	defer transport.CloseIdleConnections()
	h := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	for _, path := range []string{"/readyz", "/api/v3/client/releases?client=flutter&channel=preview"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, DefaultServerURL+path, nil)
		response, err := h.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		cancel()
		if err != nil || response.StatusCode != 200 || len(data) == 0 {
			t.Fatalf("public endpoint %s: status=%d bytes=%d error=%v", path, response.StatusCode, len(data), err)
		}
		t.Logf("public endpoint %s: status=%d bytes=%d TLS verified", path, response.StatusCode, len(data))
	}
}

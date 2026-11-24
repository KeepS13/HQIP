package client

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Exercise the packaged counter API, including selector resolution, closed
// short TCP connections, UDP and an equally busy direct route.
func TestPinnedCoreProxyStatistics(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX_EXE")
	if binary == "" {
		t.Skip("set HQIP_SINGBOX_EXE for packaged-core acceptance")
	}
	if err := (&Core{Path: binary}).verifyBinary(); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tcp.Close() })
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close() })
	go func() {
		buffer := make([]byte, 65536)
		for {
			n, addr, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			udp.WriteTo(buffer[:n], addr)
		}
	}()
	stats, _ := freePort()
	proxyTCP, _ := freePort()
	directTCP, _ := freePort()
	// TCP availability does not imply UDP availability on Windows (reserved
	// ranges and protocol-specific bindings differ). Keep both UDP reservations
	// until configuration is ready so they cannot select the same free port.
	proxyUDP, releaseProxyUDP := reserveStatsUDPPort(t)
	directUDP, releaseDirectUDP := reserveStatsUDPPort(t)
	inbound := func(tag, network string, port, target int) map[string]any {
		return map[string]any{"type": "direct", "tag": tag, "network": network, "listen": "127.0.0.1", "listen_port": port, "override_address": "127.0.0.1", "override_port": target}
	}
	config := map[string]any{
		"log":          map[string]any{"level": "error"},
		"inbounds":     []any{inbound("proxy-tcp", "tcp", proxyTCP, tcp.Addr().(*net.TCPAddr).Port), inbound("direct-tcp", "tcp", directTCP, tcp.Addr().(*net.TCPAddr).Port), inbound("proxy-udp", "udp", proxyUDP, udp.LocalAddr().(*net.UDPAddr).Port), inbound("direct-udp", "udp", directUDP, udp.LocalAddr().(*net.UDPAddr).Port)},
		"outbounds":    []any{map[string]any{"type": "selector", "tag": "proxy", "outbounds": []string{"transport"}}, map[string]any{"type": "direct", "tag": "transport"}, map[string]any{"type": "direct", "tag": "direct"}},
		"route":        map[string]any{"final": "proxy", "rules": []any{map[string]any{"inbound": []string{"direct-tcp", "direct-udp"}, "outbound": "direct", "action": "route"}}},
		"experimental": map[string]any{"v2ray_api": map[string]any{"listen": net.JoinHostPort("127.0.0.1", portString(stats)), "stats": map[string]any{"enabled": true, "outbounds": []string{"proxy"}}}},
	}
	releaseProxyUDP()
	releaseDirectUDP()
	startAcceptanceCore(t, binary, config, filepath.Join(t.TempDir(), "stats.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if _, _, err = readProxyStats(ctx, stats); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	exchange := func(network string, port int) {
		t.Helper()
		conn, err := net.DialTimeout(network, net.JoinHostPort("127.0.0.1", portString(port)), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		payload := make([]byte, 1024)
		if _, err = conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadFull(conn, payload); err != nil {
			t.Fatal(err)
		}
	}
	for range 16 {
		exchange("tcp", proxyTCP)
		exchange("tcp", directTCP)
		exchange("udp", proxyUDP)
		exchange("udp", directUDP)
	}
	up, down, err := readProxyStats(ctx, stats)
	if err != nil {
		t.Fatal(err)
	}
	if up != 32*1024 || down != 32*1024 {
		t.Fatalf("proxy counted incorrectly (direct contamination or double count): up=%d down=%d", up, down)
	}
	nextUp, nextDown, err := readProxyStats(ctx, stats)
	if err != nil || nextUp != up || nextDown != down {
		t.Fatal("reading reset cumulative counters", nextUp, nextDown, err)
	}
}

func reserveStatsUDPPort(t *testing.T) (int, func()) {
	t.Helper()
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := func() { listener.Close() }
	t.Cleanup(release)
	return listener.LocalAddr().(*net.UDPAddr).Port, release
}

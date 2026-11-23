package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"

	"github.com/KeepS13/HQIP/internal/reality"
	"github.com/apernet/quic-go"
)

var errProbeDirect = errors.New("无法确定节点直连出口")

// Only the authenticated TLS/QUIC handshake is measured. No HTTP/Hysteria auth
// request or application stream is sent, so this does not create a proxy lease.
func probeNodeHandshake(ctx context.Context, node Node) (int64, error) {
	if node.Server == "" || node.Port < 1 || node.Port > 65535 || node.SNI == "" {
		return 0, errors.New("节点地址无效")
	}
	if node.Protocol == "vless" {
		if node.Reality.Port != node.Port || node.Reality.ServerName != node.SNI {
			return 0, errors.New("Reality 节点地址无效")
		}
	} else if node.Protocol != "" && node.Protocol != "hysteria2" {
		return 0, errors.New("节点协议不支持")
	}
	// Published nodes use domains. DNS and every address attempt share the
	// caller's deadline; SNI remains the authenticated name from the profile.
	// Android resolves through its physical Network; desktop keeps the system
	// resolver. This shares the account transport's explicit DNS boundary.
	addresses, err := lookupAccountIPs(ctx, node.Server)
	if err != nil {
		return 0, err
	}
	if len(addresses) == 0 {
		return 0, errors.New("节点域名未解析到可用地址")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		delay int64
		err   error
	}
	results := make(chan result, len(addresses))
	for index, address := range addresses {
		go func() {
			// A blackholed IPv6/IPv4 address must not consume the entire test
			// budget before another address is tried. Losing probes close their
			// own sockets when cancellation interrupts their handshake.
			timer := time.NewTimer(time.Duration(index) * 250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			delay, err := probeNodeAddress(ctx, node, address)
			results <- result{delay, err}
		}()
	}
	var failures []error
	for range addresses {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case outcome := <-results:
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if outcome.err == nil {
				return outcome.delay, nil
			}
			failures = append(failures, outcome.err)
		}
	}
	return 0, errors.Join(failures...)
}

// Each attempt owns its socket and measures transport/handshake latency only,
// excluding DNS and the stagger delay. Physical-interface binding is unchanged.
func probeNodeAddress(ctx context.Context, node Node, address net.IPAddr) (int64, error) {
	if node.Protocol == "vless" {
		started := time.Now()
		conn, err := directProbeTCP(ctx, &net.TCPAddr{IP: address.IP, Zone: address.Zone, Port: node.Port})
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		if _, err = reality.Handshake(ctx, conn, node.Reality); err != nil {
			return 0, err
		}
		return max(1, time.Since(started).Milliseconds()), nil
	}
	remote := &net.UDPAddr{IP: address.IP, Zone: address.Zone, Port: node.Port}
	packet, err := directProbePacket(ctx, remote)
	if err != nil {
		return 0, err
	}
	defer packet.Close()
	return measureQUICHandshake(ctx, packet, remote, &tls.Config{ServerName: node.SNI, NextProtos: []string{"h3"}, MinVersion: tls.VersionTLS13})
}

func measureQUICHandshake(ctx context.Context, packet net.PacketConn, address net.Addr, config *tls.Config) (int64, error) {
	transport := &quic.Transport{Conn: packet}
	defer transport.Close()
	started := time.Now()
	connection, err := transport.Dial(ctx, address, config, &quic.Config{HandshakeIdleTimeout: 4 * time.Second, MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		return 0, err
	}
	delay := max(1, time.Since(started).Milliseconds())
	_ = connection.CloseWithError(0x100, "")
	return delay, nil
}

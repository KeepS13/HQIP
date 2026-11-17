package client

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/KeepS13/HQIP/internal/reality"
	"net"
	"time"

	"github.com/apernet/quic-go"
)

var errProbeDirect = errors.New("无法确定节点直连出口")

// Only the authenticated TLS/QUIC handshake is measured. No HTTP/Hysteria auth
// request or application stream is sent, so this does not create a proxy lease.
func probeNodeHandshake(ctx context.Context, node Node) (int64, error) {
	ip := net.ParseIP(node.Server)
	if ip == nil || node.Port < 1 || node.Port > 65535 || node.SNI == "" {
		return 0, errors.New("节点地址无效")
	}
	if node.Protocol == "vless" {
		if node.Reality.Port != node.Port || node.Reality.ServerName != node.SNI {
			return 0, errors.New("Reality 节点地址无效")
		}
		started := time.Now()
		conn, err := directProbeTCP(ctx, &net.TCPAddr{IP: ip, Port: node.Port})
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		if _, err = reality.Handshake(ctx, conn, node.Reality); err != nil {
			return 0, err
		}
		return max(1, time.Since(started).Milliseconds()), nil
	}
	if node.Protocol != "" && node.Protocol != "hysteria2" {
		return 0, errors.New("节点协议不支持")
	}
	address := &net.UDPAddr{IP: ip, Port: node.Port}
	packet, err := directProbePacket(ctx, address)
	if err != nil {
		return 0, err
	}
	defer packet.Close()
	return measureQUICHandshake(ctx, packet, address, &tls.Config{ServerName: node.SNI, NextProtos: []string{"h3"}, MinVersion: tls.VersionTLS13})
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

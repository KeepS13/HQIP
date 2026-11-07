//go:build !windows

package client

import (
	"context"
	"net"
)

func directProbePacket(ctx context.Context, address *net.UDPAddr) (net.PacketConn, error) {
	// The desktop transport is Windows-only. Other hosts may run loopback tests
	// but must not report potentially VPN-routed probes as direct measurements.
	if !address.IP.IsLoopback() {
		return nil, errProbeDirect
	}
	network, listen := "udp4", "127.0.0.1:0"
	if address.IP.To4() == nil {
		network, listen = "udp6", "[::1]:0"
	}
	return (&net.ListenConfig{}).ListenPacket(ctx, network, listen)
}

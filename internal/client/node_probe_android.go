//go:build android

package client

import (
	"context"
	"net"
	"syscall"
)

// Android probes use the physical network even while VpnService is active.
// The caller owns each returned socket and closes losing staggered attempts.
func directProbeTCP(ctx context.Context, address *net.TCPAddr) (net.Conn, error) {
	return directAccountTCP(ctx, "tcp", address.String())
}

func directProbePacket(ctx context.Context, address *net.UDPAddr) (net.PacketConn, error) {
	network, listen := "udp4", "0.0.0.0:0"
	if address.IP.To4() == nil {
		network, listen = "udp6", "[::]:0"
	}
	config := &net.ListenConfig{}
	if !address.IP.IsLoopback() {
		mobileNetwork.RLock()
		protect := mobileNetwork.protect
		mobileNetwork.RUnlock()
		if protect == nil {
			return nil, errProbeDirect
		}
		config.Control = func(_, _ string, raw syscall.RawConn) error {
			var protected error
			if err := raw.Control(func(fd uintptr) { protected = protect(int(fd)) }); err != nil {
				return err
			}
			return protected
		}
	}
	return config.ListenPacket(ctx, network, listen)
}

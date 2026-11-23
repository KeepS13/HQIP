//go:build windows

package client

import (
	"context"
	"net"
)

func lookupAccountIPs(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// HTTPS candidates have already been resolved. Reuse the physical-interface
// policy used by node probes; failure must not silently fall through to TUN.
// Loopback remains available for the local browser callback and regression tests.
func directAccountTCP(ctx context.Context, network, address string) (net.Conn, error) {
	resolved, err := net.ResolveTCPAddr(network, address)
	if err != nil {
		return nil, err
	}
	return directProbeTCP(ctx, resolved)
}

//go:build !windows && !android

package client

import (
	"context"
	"net"
	"time"
)

func lookupAccountIPs(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// Non-Windows hosts currently run controller tests. A future native VPN host
// must provide its own protected socket adapter before claiming TUN bypass.
func directAccountTCP(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
}

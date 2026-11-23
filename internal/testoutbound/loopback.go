// Package testoutbound provides isolated target sockets for protocol acceptance
// tests. It rejects non-loopback destinations and is never wired by an application.
package testoutbound

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

type Loopback struct{}

func address(target string) (*net.UDPAddr, error) {
	addr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return nil, err
	}
	if !addr.IP.IsLoopback() {
		return nil, errors.New("test target must be loopback")
	}
	return addr, nil
}
func (Loopback) TCP(ctx context.Context, target string) (net.Conn, error) {
	addr, err := address(target)
	if err != nil {
		return nil, err
	}
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", addr.String())
}
func (Loopback) UDP(ctx context.Context) (nodepolicy.UDPConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return nil, err
	}
	return &packet{c}, nil
}

type packet struct{ *net.UDPConn }

func (c *packet) ReadFrom(p []byte) (int, string, error) {
	n, addr, err := c.UDPConn.ReadFromUDP(p)
	if addr == nil {
		return n, "", err
	}
	return n, addr.String(), err
}
func (c *packet) WriteTo(p []byte, target string) (int, error) {
	addr, err := address(target)
	if err != nil {
		return 0, err
	}
	return c.UDPConn.WriteToUDP(p, addr)
}

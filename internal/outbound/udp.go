package outbound

import (
	"context"
	"net"
	"sync"
	"time"
)

type udpTarget struct {
	addr    *net.UDPAddr
	expires time.Time
}
type directUDP struct {
	*net.UDPConn
	ctx     context.Context
	direct  *Direct
	mu      sync.Mutex
	targets map[string]udpTarget
}

func (c *directUDP) ReadFrom(p []byte) (int, string, error) {
	_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
	n, addr, err := c.UDPConn.ReadFromUDP(p)
	if addr == nil {
		return n, "", err
	}
	return n, addr.String(), err
}

// Cache only destinations already checked by the policy, with a fixed capacity
// and TTL. Never hold mu during DNS/network calls or retain unbounded XUDP peers.
func (c *directUDP) WriteTo(p []byte, address string) (int, error) {
	c.mu.Lock()
	target, ok := c.targets[address]
	c.mu.Unlock()
	if !ok || time.Now().After(target.expires) {
		addr, err := c.direct.destination(c.ctx, address)
		if err != nil {
			return 0, err
		}
		target = udpTarget{addr: addr, expires: time.Now().Add(time.Minute)}
		c.mu.Lock()
		if len(c.targets) >= 256 {
			clear(c.targets)
		}
		c.targets[address] = target
		c.mu.Unlock()
	}
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.UDPConn.WriteToUDP(p, target.addr)
}

package nodepolicy

import "net"

// UDPConn owns one datagram socket. Addresses are host:port strings; the
// outbound must enforce destination policy before WriteTo reaches the network.
type UDPConn interface {
	ReadFrom([]byte) (int, string, error)
	WriteTo([]byte, string) (int, error)
	Close() error
}

// TCP and UDP wrap owned target sockets. The protocol remains responsible for
// closing them on cancellation/revocation. Payload is metered exactly once.
func (l *connectionLease) TCP(c net.Conn) net.Conn { return &leasedTCP{Conn: c, lease: l} }
func (l *connectionLease) UDP(c UDPConn) UDPConn   { return &leasedUDP{UDPConn: c, lease: l} }

type leasedTCP struct {
	net.Conn
	lease *connectionLease
}

func (c *leasedTCP) Read(p []byte) (int, error) {
	if len(p) > 16<<10 {
		p = p[:16<<10]
	}
	n, e := c.Conn.Read(p)
	if n > 0 {
		if err := c.lease.consume(false, n); err != nil {
			return 0, err
		}
	}
	return n, e
}
func (c *leasedTCP) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > 16<<10 {
			n = 16 << 10
		}
		if err := c.lease.consume(true, n); err != nil {
			return total, err
		}
		written, err := c.Conn.Write(p[:n])
		total += written
		if err != nil {
			return total, err
		}
		p = p[written:]
		if written == 0 {
			return total, net.ErrClosed
		}
	}
	return total, nil
}

type leasedUDP struct {
	UDPConn
	lease *connectionLease
}

func (c *leasedUDP) ReadFrom(p []byte) (int, string, error) {
	n, addr, e := c.UDPConn.ReadFrom(p)
	if n > 0 {
		if err := c.lease.consume(false, n); err != nil {
			return 0, addr, err
		}
	}
	return n, addr, e
}
func (c *leasedUDP) WriteTo(p []byte, addr string) (int, error) {
	if err := c.lease.consume(true, len(p)); err != nil {
		return 0, err
	}
	return c.UDPConn.WriteTo(p, addr)
}

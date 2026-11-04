package server

import "net"

// Wrapping target sockets applies policy before bytes leave either side and
// meters UDP payload once, before HY2 fragmentation/retransmission framing.
type leasedOutbound struct {
	base  Outbound
	lease *connectionLease
}

func (o *leasedOutbound) TCP(addr string) (net.Conn, error) {
	c, e := o.base.TCP(addr)
	if e != nil {
		return nil, e
	}
	return &leasedTCP{Conn: c, lease: o.lease}, nil
}
func (o *leasedOutbound) UDP(addr string) (UDPConn, error) {
	c, e := o.base.UDP(addr)
	if e != nil {
		return nil, e
	}
	return &leasedUDP{UDPConn: c, lease: o.lease}, nil
}
func (o *leasedOutbound) CheckUDP(addr string) error { return o.base.CheckUDP(addr) }

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

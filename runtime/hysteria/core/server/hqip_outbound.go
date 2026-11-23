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
	return o.lease.TCP(c), nil
}
func (o *leasedOutbound) UDP(addr string) (UDPConn, error) {
	c, e := o.base.UDP(addr)
	if e != nil {
		return nil, e
	}
	return o.lease.UDP(c), nil
}
func (o *leasedOutbound) CheckUDP(addr string) error { return o.base.CheckUDP(addr) }

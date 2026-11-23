package server

import (
	"net"
	"sync"
)

// Wrapping target sockets applies policy before bytes leave either side and
// meters UDP payload once, before HY2 fragmentation/retransmission framing.
type leasedOutbound struct {
	base  Outbound
	lease *connectionLease
}

func (o *leasedOutbound) TCP(addr string) (net.Conn, error) {
	episode, err := o.lease.acquire()
	if err != nil {
		return nil, err
	}
	c, e := o.base.TCP(addr)
	if e != nil {
		o.lease.release(episode)
		return nil, e
	}
	return &activityTCP{Conn: episode.lease.TCP(c), release: func() { o.lease.release(episode) }}, nil
}
func (o *leasedOutbound) UDP(addr string) (UDPConn, error) {
	episode, err := o.lease.acquire()
	if err != nil {
		return nil, err
	}
	c, e := o.base.UDP(addr)
	if e != nil {
		o.lease.release(episode)
		return nil, e
	}
	return &activityUDP{UDPConn: episode.lease.UDP(c), release: func() { o.lease.release(episode) }}, nil
}
func (o *leasedOutbound) CheckUDP(addr string) error { return o.base.CheckUDP(addr) }

type activityTCP struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *activityTCP) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

type activityUDP struct {
	UDPConn
	once    sync.Once
	release func()
}

func (c *activityUDP) Close() error {
	err := c.UDPConn.Close()
	c.once.Do(c.release)
	return err
}

package outbound

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

// Client is one credential/source-IP authorization shared by VLESS streams.
// It owns all target sockets and closes them before sending a cumulative final.
type Client interface {
	TCP(string) (net.Conn, error)
	UDP() (PacketConn, error)
	Close() error
	Snapshot() nodepolicy.Snapshot
}

type PacketConn interface {
	Send([]byte, string) error
	Receive() ([]byte, string, error)
	Close() error
}

// Session's mutex protects socket ownership, never network work or lease calls.
// Lease revocation calls shutdown only; Finish runs separately to avoid lock
// inversion with the guard's renewal/counter locks.
type Session struct {
	ctx       context.Context
	cancel    context.CancelFunc
	connector Connector
	lease     *nodepolicy.Lease
	mu        sync.Mutex
	closed    bool
	sockets   map[io.Closer]struct{}
	done      chan struct{}
}

func NewSession(ctx context.Context, admission *nodepolicy.Admission, credential, peerIP string, connector Connector) (Client, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{ctx: ctx, cancel: cancel, connector: connector, sockets: make(map[io.Closer]struct{}), done: make(chan struct{})}
	lease, _, err := admission.Open(ctx, nodepolicy.NewConnectionID(), credential, peerIP, s.shutdown)
	if err != nil {
		cancel()
		admission.Close()
		return nil, err
	}
	s.lease = lease
	go func() { <-ctx.Done(); s.shutdown(); lease.Finish(); admission.Close(); close(s.done) }()
	if ctx.Err() != nil {
		_ = s.Close()
		return nil, net.ErrClosed
	}
	return s, nil
}

func (s *Session) track(c io.Closer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.sockets[c] = struct{}{}
	return true
}

func (s *Session) forget(c io.Closer) { s.mu.Lock(); delete(s.sockets, c); s.mu.Unlock() }

func (s *Session) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	sockets := make([]io.Closer, 0, len(s.sockets))
	for c := range s.sockets {
		sockets = append(sockets, c)
	}
	s.mu.Unlock()
	for _, c := range sockets {
		_ = c.Close()
	}
}

func (s *Session) Close() error { s.shutdown(); <-s.done; return nil }

func (s *Session) Snapshot() nodepolicy.Snapshot { return s.lease.Snapshot() }

func (s *Session) TCP(address string) (net.Conn, error) {
	if s.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	c, err := s.connector.TCP(s.ctx, address)
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, net.ErrClosed
		}
		return nil, err
	}
	owned := &sessionTCP{Conn: s.lease.TCP(c), session: s}
	if !s.track(owned) {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	return owned, nil
}

func (s *Session) UDP() (PacketConn, error) {
	if s.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	c, err := s.connector.UDP(s.ctx)
	if err != nil {
		return nil, err
	}
	owned := &sessionUDP{UDPConn: s.lease.UDP(c), session: s}
	if !s.track(owned) {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	return owned, nil
}

type sessionTCP struct {
	net.Conn
	session *Session
	once    sync.Once
}

func (c *sessionTCP) Close() error {
	var err error
	c.once.Do(func() { err = c.Conn.Close(); c.session.forget(c) })
	return err
}

type sessionUDP struct {
	nodepolicy.UDPConn
	session *Session
	once    sync.Once
}

func (c *sessionUDP) Close() error {
	var err error
	c.once.Do(func() { err = c.UDPConn.Close(); c.session.forget(c) })
	return err
}
func (c *sessionUDP) Send(p []byte, addr string) error { _, err := c.WriteTo(p, addr); return err }
func (c *sessionUDP) Receive() ([]byte, string, error) {
	p := make([]byte, 65535)
	n, addr, err := c.ReadFrom(p)
	return p[:n], addr, err
}

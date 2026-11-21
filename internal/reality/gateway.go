// Package reality provides a TCP VLESS/Reality ingress backed exclusively by
// the node's existing HY2 service. It cannot dial proxy destinations directly:
// all TCP and UDP payloads pass through the existing admission, quota, rate,
// revocation and accounting guards exactly once.
package reality

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	hy "github.com/apernet/hysteria/core/v2/client"
	hyErrors "github.com/apernet/hysteria/core/v2/errors"
	utls "github.com/metacubex/utls"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// Config is immutable after New. Resolve returns the original HY2 credential
// for an authenticated VLESS alias, or an error without disclosing credentials.
// Connect must establish an admission-checked backend, never a direct outbound.
type Config struct {
	TLS     *utls.RealityConfig
	Resolve func(context.Context, string) (string, error)
	Connect func(context.Context, string, string) (hy.Client, error)
	Idle    time.Duration
}

type group struct {
	ready  chan struct{}
	client hy.Client
	err    error
	refs   int
	timer  *time.Timer
}

// Gateway owns the listener, ingress sockets and backend clients. mu protects
// ownership only; authentication, handshakes and Close never run while held.
type Gateway struct {
	cfg    Config
	mu     sync.Mutex
	groups map[string]*group
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

func New(cfg Config) (*Gateway, error) {
	if cfg.TLS == nil || cfg.Resolve == nil || cfg.Connect == nil {
		return nil, errors.New("incomplete Reality gateway configuration")
	}
	if cfg.Idle <= 0 {
		cfg.Idle = 5 * time.Second
	}
	return &Gateway{cfg: cfg, groups: map[string]*group{}, conns: map[net.Conn]struct{}{}}, nil
}

// Serve blocks until cancellation or a listener error, then closes and joins all
// ingress handlers. A bounded connection set limits unauthenticated resource use.
func (g *Gateway) Serve(ctx context.Context, listener net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = listener.Close(); g.Close() })
	defer stop()
	defer func() { _ = listener.Close(); g.Close(); g.wg.Wait() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		g.mu.Lock()
		if g.closed || len(g.conns) >= 4096 {
			g.mu.Unlock()
			_ = conn.Close()
			continue
		}
		g.conns[conn] = struct{}{}
		g.wg.Add(1)
		g.mu.Unlock()
		go func() {
			defer g.wg.Done()
			defer conn.Close()
			defer func() { g.mu.Lock(); delete(g.conns, conn); g.mu.Unlock() }()
			g.serveConn(ctx, conn)
		}()
	}
}

func (g *Gateway) serveConn(ctx context.Context, raw net.Conn) {
	// The deadline also bounds Reality's unauthenticated fallback copy.
	_ = raw.SetDeadline(time.Now().Add(12 * time.Second))
	conn, err := utls.RealityServer(ctx, raw, g.cfg.TLS)
	if err != nil {
		return
	}
	// The upstream parser bounds the header (one-byte addon/domain lengths).
	// Capture exactly that header, leaving all payload in TLS-owned buffers so
	// Vision can safely switch to the raw socket after its padding handshake.
	var header bytes.Buffer
	request, err := vless.ReadRequest(io.TeeReader(conn, &header))
	if err != nil || (request.Flow != "" && (request.Flow != vless.FlowVision || !VisionEnabled)) {
		return
	}
	h := hex.EncodeToString(request.UUID[:])
	id := h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	host, _, err := net.SplitHostPort(raw.RemoteAddr().String())
	if err != nil {
		return
	}
	// VLESS opens streams, not a QUIC VPN session. Streams sharing a credential
	// and source IP use one local HY2 connection/lease. Different credentials or
	// source IPs keep separate local meters; central admission deduplicates the
	// source IP across VLESS credentials/nodes belonging to the same subscription.
	key := id + "@" + host
	backend, err := g.acquire(ctx, key, id, host)
	if err != nil {
		return
	}
	defer g.release(key, backend)
	_ = raw.SetDeadline(time.Time{})
	handler := &forwarder{backend: backend.client, failed: func(err error) {
		var closed hyErrors.ClosedError
		if errors.As(err, &closed) {
			g.invalidate(key, backend)
		}
	}}
	service := vless.NewService[string](logger.NOP(), handler)
	// Each socket owns an immutable service. Accept legacy empty-flow clients
	// during rolling upgrades; new profiles select Vision only after capability
	// reporting. Both paths use the same authenticated, guarded backend above.
	service.UpdateUsers([]string{id}, []string{id}, []string{request.Flow})
	replay := &replayConn{Conn: conn, prefix: bytes.NewReader(header.Bytes())}
	_ = service.NewConnection(ctx, replay, M.ParseSocksaddr(raw.RemoteAddr().String()), nil)
}

type replayConn struct {
	net.Conn
	prefix *bytes.Reader
}

// Vision's TLS adapter may unwrap only after the VLESS parser consumes the
// replayed header. Unwrapping earlier would silently discard protocol bytes.
func (c *replayConn) ReaderReplaceable() bool { return c.prefix.Len() == 0 }
func (c *replayConn) Upstream() any           { return c.Conn }

func (c *replayConn) Read(p []byte) (int, error) {
	if c.prefix.Len() > 0 {
		return c.prefix.Read(p)
	}
	return c.Conn.Read(p)
}

func (g *Gateway) acquire(ctx context.Context, key, id, clientIP string) (*group, error) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, net.ErrClosed
	}
	if existing := g.groups[key]; existing != nil {
		existing.refs++
		if existing.timer != nil {
			existing.timer.Stop()
		}
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			g.release(key, existing)
			return nil, ctx.Err()
		case <-existing.ready:
			if existing.err != nil {
				g.release(key, existing)
				return nil, existing.err
			}
			return existing, nil
		}
	}
	if len(g.groups) >= 4096 {
		g.mu.Unlock()
		return nil, errors.New("Reality backend capacity reached")
	}
	b := &group{ready: make(chan struct{}), refs: 1}
	g.groups[key] = b
	g.mu.Unlock()
	password, err := g.cfg.Resolve(ctx, id)
	if err == nil {
		b.client, err = g.cfg.Connect(ctx, password, clientIP)
	}
	g.mu.Lock()
	if g.closed && err == nil {
		err = net.ErrClosed
	}
	b.err = err
	if err != nil {
		delete(g.groups, key)
	}
	close(b.ready)
	g.mu.Unlock()
	if err != nil {
		if b.client != nil {
			_ = b.client.Close()
		}
		return nil, err
	}
	return b, nil
}

// A revoked or restarted backend cannot be reused by newly arriving streams.
// Destination-specific dial failures do not invalidate other streams' leases.
func (g *Gateway) invalidate(key string, b *group) {
	g.mu.Lock()
	owned := g.groups[key] == b
	if owned {
		delete(g.groups, key)
		if b.timer != nil {
			b.timer.Stop()
		}
	}
	g.mu.Unlock()
	if owned {
		_ = b.client.Close()
	}
}

func (g *Gateway) release(key string, b *group) {
	g.mu.Lock()
	b.refs--
	if b.refs == 0 && g.groups[key] == b && !g.closed {
		b.timer = time.AfterFunc(g.cfg.Idle, func() {
			g.mu.Lock()
			remove := b.refs == 0 && g.groups[key] == b
			if remove {
				delete(g.groups, key)
			}
			g.mu.Unlock()
			if remove && b.client != nil {
				_ = b.client.Close()
			}
		})
	}
	g.mu.Unlock()
}

// Close is idempotent and never waits for network work while holding mu.
func (g *Gateway) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	conns := make([]net.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	backends := make([]hy.Client, 0, len(g.groups))
	for _, b := range g.groups {
		if b.timer != nil {
			b.timer.Stop()
		}
		select {
		case <-b.ready:
			if b.client != nil {
				backends = append(backends, b.client)
			}
		default: // The acquiring goroutine closes its client after observing closed.
		}
	}
	g.groups = map[string]*group{}
	g.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	for _, b := range backends {
		_ = b.Close()
	}
}

type forwarder struct {
	backend hy.Client
	failed  func(error)
}

// Both copy directions must finish before the ingress group reference is
// released. Closing either side interrupts the other direction immediately.
func (f *forwarder) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	defer conn.Close()
	remote, err := f.backend.TCP(destination.String())
	if err != nil {
		f.failed(err)
	}
	if err == nil {
		defer remote.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close(); _ = remote.Close() })
		defer stop()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, conn); _ = remote.Close(); _ = conn.Close(); close(done) }()
		_, err = io.Copy(conn, remote)
		_ = conn.Close()
		_ = remote.Close()
		<-done
	}
	if onClose != nil {
		onClose(err)
	}
}

func (f *forwarder) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	defer conn.Close()
	remote, err := f.backend.UDP()
	if err != nil {
		f.failed(err)
	}
	if err == nil {
		defer remote.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close(); _ = remote.Close() })
		defer stop()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer conn.Close()
			defer remote.Close()
			buffer := buf.NewSize(65535)
			defer buffer.Release()
			for {
				buffer.Reset()
				dest, e := conn.ReadPacket(buffer)
				if e != nil || remote.Send(buffer.Bytes(), dest.String()) != nil {
					return
				}
			}
		}()
		for {
			var data []byte
			var addr string
			data, addr, err = remote.Receive()
			if err != nil {
				break
			}
			// Packet writers consume the buffer, including required VLESS/mux
			// framing headroom. A slice wrapper would panic in non-mux UDP.
			headroom := N.CalculateFrontHeadroom(conn)
			packet := buf.NewSize(headroom + len(data))
			packet.Advance(headroom)
			_, _ = packet.Write(data)
			if err = conn.WritePacket(packet, M.ParseSocksaddr(addr)); err != nil {
				break
			}
		}
		_ = conn.Close()
		_ = remote.Close()
		<-done
	}
	if onClose != nil {
		onClose(err)
	}
}

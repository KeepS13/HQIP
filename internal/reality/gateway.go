// Package reality provides VLESS/Reality ingress over independently guarded
// outbound sessions. TCP and UDP targets do not traverse a local HY2 tunnel.
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

	"github.com/KeepS13/HQIP/internal/outbound"
	utls "github.com/metacubex/utls"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

// Config is immutable after New. Resolve returns the original subscription credential
// for an authenticated VLESS alias, or an error without disclosing credentials.
// Connect must establish an authorized, metered outbound session before dialing.
type Config struct {
	TLS     *utls.RealityConfig
	Resolve func(context.Context, string) (string, error)
	Connect func(context.Context, string, string) (outbound.Client, error)
	Idle    time.Duration
}

type group struct {
	ready  chan struct{}
	client outbound.Client
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
	// and source IP use one independent outbound session/lease. Different credentials or
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
		if errors.Is(err, net.ErrClosed) {
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
	backends := make([]outbound.Client, 0, len(g.groups))
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

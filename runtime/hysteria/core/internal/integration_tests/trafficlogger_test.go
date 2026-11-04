package integration_tests

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/core/v2/internal/integration_tests/mocks"
	"github.com/apernet/hysteria/core/v2/server"
)

// TestClientServerTrafficLoggerTCP tests that the traffic logger is correctly called for TCP connections,
// and that the client is disconnected when the traffic logger returns false.
func TestClientServerTrafficLoggerTCP(t *testing.T) {
	// Create server
	udpConn, udpAddr, err := serverConn()
	assert.NoError(t, err)
	serverOb := mocks.NewMockOutbound(t)
	auth := mocks.NewMockAuthenticator(t)
	auth.EXPECT().Authenticate(mock.Anything, mock.Anything, mock.Anything).Return(true, "nobody")
	trafficLogger := mocks.NewMockTrafficLogger(t)
	traces := newTrafficTraceRecorder(trafficLogger)
	s, err := server.NewServer(&server.Config{
		TLSConfig:     serverTLSConfig(),
		Conn:          udpConn,
		Outbound:      serverOb,
		Authenticator: auth,
		TrafficLogger: traces,
	})
	assert.NoError(t, err)
	defer s.Close()
	go s.Serve()

	// Create client
	trafficLogger.EXPECT().LogOnlineState("nobody", true).Return().Once()
	c, _, err := client.NewClient(&client.Config{
		ServerAddr: udpAddr,
		TLSConfig:  client.TLSConfig{InsecureSkipVerify: true},
	})
	assert.NoError(t, err)
	defer c.Close()

	addr := "dontcare.cc:4455"

	sobConn := mocks.NewMockConn(t)
	sobConnCh := make(chan []byte, 1)
	sobConnChCloseFunc := sync.OnceFunc(func() { close(sobConnCh) })
	sobConn.EXPECT().Read(mock.Anything).RunAndReturn(func(bs []byte) (int, error) {
		b := <-sobConnCh
		if b == nil {
			return 0, io.EOF
		} else {
			return copy(bs, b), nil
		}
	})
	sobConn.EXPECT().Close().RunAndReturn(func() error {
		sobConnChCloseFunc()
		return nil
	})
	serverOb.EXPECT().TCP(addr).Return(sobConn, nil).Once()

	conn, err := c.TCP(addr)
	assert.NoError(t, err)
	trace := awaitTrafficTrace(t, traces.traced)
	require.NotNil(t, trace.stats)
	assert.Equal(t, "nobody", trace.authID)
	assert.Equal(t, server.StreamStateInitial, trace.initialState)

	// Client reads from server
	trafficLogger.EXPECT().LogTraffic("nobody", uint64(0), uint64(11)).Return(true).Once()
	sobConnCh <- []byte("knock knock")
	buf := make([]byte, 100)
	n, err := conn.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 11, n)
	assert.Equal(t, "knock knock", string(buf[:n]))

	// Client writes to server
	trafficLogger.EXPECT().LogTraffic("nobody", uint64(12), uint64(0)).Return(true).Once()
	written := make(chan struct{})
	sobConn.EXPECT().Write([]byte("who is there")).Run(func([]byte) { close(written) }).Return(12, nil).Once()
	n, err = conn.Write([]byte("who is there"))
	assert.NoError(t, err)
	assert.Equal(t, 12, n)
	awaitTrafficTrace(t, written)

	// Client reads from server again but blocked
	trafficLogger.EXPECT().LogTraffic("nobody", uint64(0), uint64(4)).Return(false).Once()
	offline := make(chan struct{})
	trafficLogger.EXPECT().LogOnlineState("nobody", false).Run(func(string, bool) { close(offline) }).Return().Once()
	sobConnCh <- []byte("nope")
	n, err = conn.Read(buf)
	assert.Zero(t, n)
	assert.Error(t, err)

	// The client should be disconnected
	_, err = c.TCP("whatever")
	assert.Error(t, err)
	awaitTrafficTrace(t, offline)
	untraced := awaitTrafficTrace(t, traces.untraced)
	// Compare stream identity without recursively formatting the live QUIC object.
	if untraced != trace.stream {
		t.Error("UntraceStream did not receive the traced stream")
	}
	// Closed is stored by the final defer, after UntraceStream. Read through the
	// supported atomic API instead of letting testify reflect into atomic.Value.
	require.Eventually(t, func() bool { return trace.stats.State.Load() == server.StreamStateClosed }, 3*time.Second, time.Millisecond)
	assert.Equal(t, int32(1), traces.traceCalls.Load())
	assert.Equal(t, int32(1), traces.untraceCalls.Load())
	assert.Equal(t, addr, trace.stats.ReqAddr.Load())
	assert.Equal(t, uint64(12), trace.stats.Tx.Load())
	assert.Equal(t, uint64(15), trace.stats.Rx.Load())
}

// TestClientServerTrafficLoggerUDP tests that the traffic logger is correctly called for UDP sessions,
// and that the client is disconnected when the traffic logger returns false.
func TestClientServerTrafficLoggerUDP(t *testing.T) {
	// Create server
	udpConn, udpAddr, err := serverConn()
	assert.NoError(t, err)
	serverOb := mocks.NewMockOutbound(t)
	auth := mocks.NewMockAuthenticator(t)
	auth.EXPECT().Authenticate(mock.Anything, mock.Anything, mock.Anything).Return(true, "nobody")
	trafficLogger := mocks.NewMockTrafficLogger(t)
	s, err := server.NewServer(&server.Config{
		TLSConfig:     serverTLSConfig(),
		Conn:          udpConn,
		Outbound:      serverOb,
		Authenticator: auth,
		TrafficLogger: trafficLogger,
	})
	assert.NoError(t, err)
	defer s.Close()
	go s.Serve()

	// Create client
	trafficLogger.EXPECT().LogOnlineState("nobody", true).Return().Once()
	c, _, err := client.NewClient(&client.Config{
		ServerAddr: udpAddr,
		TLSConfig:  client.TLSConfig{InsecureSkipVerify: true},
	})
	assert.NoError(t, err)
	defer c.Close()

	addr := "shady.org:43211"

	sobConn := mocks.NewMockUDPConn(t)
	sobConnCh := make(chan []byte, 1)
	sobConnChCloseFunc := sync.OnceFunc(func() { close(sobConnCh) })
	sobConn.EXPECT().ReadFrom(mock.Anything).RunAndReturn(func(bs []byte) (int, string, error) {
		b := <-sobConnCh
		if b == nil {
			return 0, "", io.EOF
		} else {
			return copy(bs, b), addr, nil
		}
	})
	sobConn.EXPECT().Close().RunAndReturn(func() error {
		sobConnChCloseFunc()
		return nil
	})
	serverOb.EXPECT().UDP(addr).Return(sobConn, nil).Once()

	conn, err := c.UDP()
	assert.NoError(t, err)

	// Client writes to server
	trafficLogger.EXPECT().LogTraffic("nobody", uint64(9), uint64(0)).Return(true).Once()
	written := make(chan struct{})
	sobConn.EXPECT().WriteTo([]byte("small sad"), addr).Run(func([]byte, string) { close(written) }).Return(9, nil).Once()
	err = conn.Send([]byte("small sad"), addr)
	assert.NoError(t, err)
	awaitTrafficTrace(t, written)

	// Client reads from server
	trafficLogger.EXPECT().LogTraffic("nobody", uint64(0), uint64(7)).Return(true).Once()
	sobConnCh <- []byte("big mad")
	bs, rAddr, err := conn.Receive()
	assert.NoError(t, err)
	assert.Equal(t, rAddr, addr)
	assert.Equal(t, "big mad", string(bs))

	// Client reads from server again but blocked
	trafficLogger.EXPECT().LogTraffic("nobody", uint64(0), uint64(4)).Return(false).Once()
	offline := make(chan struct{})
	trafficLogger.EXPECT().LogOnlineState("nobody", false).Run(func(string, bool) { close(offline) }).Return().Once()
	sobConnCh <- []byte("nope")
	bs, rAddr, err = conn.Receive()
	assert.Equal(t, err, io.EOF)
	assert.Empty(t, rAddr)
	assert.Empty(t, bs)

	// The client should be disconnected
	_, err = c.UDP()
	assert.Error(t, err)
	awaitTrafficTrace(t, offline)
}

type recordedTrafficTrace struct {
	stream       server.HyStream
	stats        *server.StreamStats
	authID       string
	initialState server.StreamState
}

// Testify records and formats every argument even for mock.Anything. Streams and
// StreamStats remain mutable after UntraceStream, so never hand them to a mock.
// Keep the original traffic/online mock assertions and observe trace lifecycle
// separately, using channels for publication and atomics for mutable statistics.
type trafficTraceRecorder struct {
	*mocks.MockTrafficLogger
	traced       chan recordedTrafficTrace
	untraced     chan server.HyStream
	traceCalls   atomic.Int32
	untraceCalls atomic.Int32
}

func newTrafficTraceRecorder(logger *mocks.MockTrafficLogger) *trafficTraceRecorder {
	return &trafficTraceRecorder{MockTrafficLogger: logger, traced: make(chan recordedTrafficTrace, 1), untraced: make(chan server.HyStream, 1)}
}

func (r *trafficTraceRecorder) TraceStream(stream server.HyStream, stats *server.StreamStats) {
	r.traceCalls.Add(1)
	value := recordedTrafficTrace{stream: stream, stats: stats}
	if stats != nil {
		value.authID = stats.AuthID
		value.initialState = stats.State.Load()
	}
	select {
	case r.traced <- value:
	default:
	}
}

func (r *trafficTraceRecorder) UntraceStream(stream server.HyStream) {
	r.untraceCalls.Add(1)
	select {
	case r.untraced <- stream:
	default:
	}
}

func awaitTrafficTrace[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server traffic logger callback")
		var zero T
		return zero
	}
}

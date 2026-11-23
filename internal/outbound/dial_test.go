package outbound

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func dialTargets() []*net.UDPAddr {
	return []*net.UDPAddr{{IP: net.IPv4(192, 0, 2, 1), Port: 443}, {IP: net.IPv4(192, 0, 2, 2), Port: 443}, {IP: net.IPv4(192, 0, 2, 3), Port: 443}}
}

func TestTCPFastAddressDoesNotCreateFallbackConnections(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	var calls atomic.Int32
	conn, err := dialTCPAddresses(context.Background(), dialTargets(), func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return left, nil
	})
	if err != nil || conn != left {
		t.Fatal("lost first connection", err)
	}
	time.Sleep(300 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("healthy address caused redundant dials", calls.Load())
	}
}

func TestTCPBlackholeDoesNotDelayOtherAddress(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	targets := dialTargets()
	cancelled := make(chan struct{})
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialTCPAddresses(ctx, targets, func(ctx context.Context, _, address string) (net.Conn, error) {
		if address == targets[0].String() {
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		}
		return left, nil
	})
	if err != nil || conn != left || time.Since(started) > time.Second {
		t.Fatal("blackhole delayed healthy address", err, time.Since(started))
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("losing dial was not cancelled")
	}
}

type watchedConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *watchedConn) Close() error { c.once.Do(func() { close(c.closed) }); return c.Conn.Close() }

func TestTCPCancellationClosesLateSocket(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	owned := &watchedConn{Conn: left, closed: make(chan struct{})}
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := dialTCPAddresses(ctx, dialTargets()[:1], func(context.Context, string, string) (net.Conn, error) {
			close(started)
			<-release
			return owned, nil
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
	close(release)
	select {
	case <-owned.closed:
	case <-time.After(time.Second):
		t.Fatal("late success leaked socket")
	}
}

func TestTCPFallbackConcurrencyIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	arrived := make(chan struct{}, 3)
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		_, err := dialTCPAddresses(ctx, dialTargets(), func(ctx context.Context, _, _ string) (net.Conn, error) {
			calls.Add(1)
			arrived <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		done <- err
	}()
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("fallback missing")
		}
	}
	select {
	case <-arrived:
		t.Fatal("more than two concurrent dials")
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || calls.Load() != 2 {
		t.Fatal("unbounded dial attempts", calls.Load(), err)
	}
}

func TestTCPFailedCandidatesAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	errRefused := errors.New("refused")
	_, err := dialTCPAddresses(context.Background(), dialTargets(), func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errRefused
	})
	if !errors.Is(err, errRefused) || calls.Load() != 3 {
		t.Fatal("lost failures or repeated candidate", calls.Load(), err)
	}
}

func TestTCPFirstAddressRetainsCallerBudget(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := dialTCPAddresses(ctx, dialTargets(), func(attempt context.Context, _, _ string) (net.Conn, error) {
		deadline, ok := attempt.Deadline()
		if !ok || time.Until(deadline) < 9*time.Second {
			t.Error("fallback shortened the primary address budget")
		}
		return left, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

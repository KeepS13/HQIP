package outbound

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// dialTCPAddresses owns candidate sockets until exactly one is transferred to
// the caller. Only two dials can run together; a fast first address makes no
// fallback connection. The primary keeps the caller's full budget. Intermediate
// fallbacks get 3 seconds to leave room for later candidates; the last one keeps
// the remaining budget. Candidates must already pass the caller's egress policy.
func dialTCPAddresses(ctx context.Context, targets []*net.UDPAddr, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan *net.UDPAddr, len(targets))
	for _, target := range targets {
		jobs <- target
	}
	close(jobs)
	type outcome struct {
		conn net.Conn
		err  error
	}
	results := make(chan outcome) // Unbuffered: losing sockets cannot be orphaned.
	fallback := make(chan struct{})
	var firstFailure sync.Once
	var workers sync.WaitGroup
	for worker := 0; worker < min(2, len(targets)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if worker == 1 {
				timer := time.NewTimer(250 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-fallback:
				case <-timer.C:
				}
			}
			for target := range jobs {
				if ctx.Err() != nil {
					return
				}
				var attempt context.Context
				var stop context.CancelFunc
				if target == targets[0] || len(jobs) == 0 {
					attempt, stop = context.WithCancel(ctx)
				} else {
					attempt, stop = context.WithTimeout(ctx, 3*time.Second)
				}
				conn, err := dial(attempt, "tcp4", target.String())
				stop()
				if err != nil {
					if conn != nil {
						_ = conn.Close()
						conn = nil
					}
					firstFailure.Do(func() { close(fallback) })
				}
				select {
				case results <- outcome{conn, err}:
					if err == nil {
						return
					}
				case <-ctx.Done():
					if conn != nil {
						_ = conn.Close()
					}
					return
				}
			}
		}()
	}
	go func() { workers.Wait(); close(results) }()
	var failures []error
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result, ok := <-results:
			if !ok {
				return nil, errors.Join(failures...)
			}
			if result.err != nil {
				failures = append(failures, result.err)
				continue
			}
			if err := ctx.Err(); err != nil {
				_ = result.conn.Close()
				return nil, err
			}
			return result.conn, nil
		}
	}
}

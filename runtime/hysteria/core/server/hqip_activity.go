package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

// connectionLease owns one authenticated QUIC transport. Only live TCP targets
// and UDP sessions hold references. mu serializes admission/final so a new
// episode cannot race ahead of the previous episode's durable release. No
// global, traffic-sampling or database lock is held here. The credential lives
// only as long as this transport; it is revalidated for every new episode.
type connectionLease struct {
	activity                  func(bool) // Set before publishing the authenticated transport.
	mu                        sync.Mutex
	admission                 *Admission
	ctx                       context.Context
	password, peerIP, account string
	closeConnection           func()
	current                   *activityLease
	closed                    bool
}

type activityLease struct {
	lease *nodepolicy.Lease
	refs  int
	idle  atomic.Bool // A normal final must not abort queued QUIC response bytes.
}

func (l *connectionLease) acquire() (*activityLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	if l.current == nil {
		e := &activityLease{}
		lease, account, err := l.admission.Open(l.ctx, randomConnectionID(), l.password, l.peerIP, func() {
			if !e.idle.Load() {
				l.closeConnection()
			}
		})
		if err != nil {
			return nil, err
		}
		if account != l.account {
			lease.Finish()
			return nil, errors.New("transport accounting identity changed")
		}
		e.lease = lease
		l.current = e
		if l.activity != nil {
			l.activity(true)
		}
	}
	l.current.refs++
	return l.current, nil
}

// Called exactly once after the target socket has closed (also on failed dial).
// Final stops renewal and durably delivers real counters. It does not clear
// central or local accounting state, and unconfirmed usage remains protected.
func (l *connectionLease) release(e *activityLease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current != e {
		return
	}
	e.refs--
	if e.refs == 0 {
		e.idle.Store(true)
		e.lease.Finish()
		l.current = nil
		if l.activity != nil {
			l.activity(false)
		}
	}
}

// QUIC termination invalidates all target sockets through the original callback.
// Late socket closes cannot finish a replacement episode or decrement it twice.
func (l *connectionLease) finish() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.password = ""
	if l.current != nil {
		l.current.lease.Finish()
		l.current = nil
		if l.activity != nil {
			l.activity(false)
		}
	}
}

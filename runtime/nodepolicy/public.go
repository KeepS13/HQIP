package nodepolicy

import "context"

// Lease is a single forwarding authorization. Call Finish once the transport
// closes; it is idempotent. A nil lease permits the unguarded upstream HY2 mode.
type Lease = connectionLease

// NewConnectionID returns a cryptographically random 256-bit connection identity.
// IDs and leases must never be resumed after their owning process restarts.
func NewConnectionID() string { return randomConnectionID() }

// Open authenticates through the local Agent and starts bounded renewal. Peer IP
// must come from the ingress socket. closeConnection must close every associated
// stream without calling Lease methods; errors never authorize a fallback path.
func (a *Admission) Open(ctx context.Context, id, password, peerIP string, closeConnection func()) (*Lease, string, error) {
	return a.open(ctx, id, password, peerIP, closeConnection)
}

// Finish revokes local forwarding before delivering the cumulative final. The
// Agent durably queues accepted usage; an unavailable Agent cannot free budget.
func (l *connectionLease) Finish() { l.finish() }

// Close releases idle HTTP connections when a short-lived protocol adapter ends.
// Long-lived HY2 admission clients may instead be shared for the server lifetime.
func (a *Admission) Close() { a.client.CloseIdleConnections() }

// Snapshot contains raw payload telemetry, never a second charging authority.
// Closed counters are immutable and can be removed after durable collection.
type Snapshot struct {
	ID, AccountingID string
	Upload, Download int64
	Closed           bool
}

func (l *connectionLease) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Snapshot{ID: l.grant.LeaseID, AccountingID: l.grant.AccountingID, Upload: l.usedUp, Download: l.usedDown, Closed: l.closed}
}

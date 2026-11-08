package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"
)

type leaseTransactionScope struct {
	store *Store
	conn  *sql.Conn
	key   string
}

// Quota/rate updates for a single subscription necessarily serialize. Acquire
// that subscription's session lock before BEGIN so a queued writer starts with
// a fresh Serializable snapshot. A transaction-level advisory lock would take
// the snapshot before waiting and still collide with the writer ahead of it.
// The lookup is only a routing hint: the transaction repeats all authorization
// and accounting reads. Other subscriptions use independent database lock keys.
func (s *Store) leaseTransactionScope(query string, args ...any) (*leaseTransactionScope, error) {
	scope := &leaseTransactionScope{store: s}
	if !s.Postgres() {
		return scope, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var subscription string
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&subscription); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Missing credentials and immutable settled receipts retain the
			// authoritative error/replay handling inside the transaction.
			return scope, nil
		}
		return nil, err
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	scope.key = "hqip:lease-subscription:" + subscription
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtextextended(?,0))", scope.key); err != nil {
		// A failed response does not prove the session lock was never granted.
		// Never return such a connection to the pool with an unknown lock state.
		conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
		return nil, err
	}
	scope.conn = conn
	return scope, nil
}

func (scope *leaseTransactionScope) close() {
	if scope.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	err := scope.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock(hashtextextended(?,0))", scope.key).Scan(&unlocked)
	if err != nil || !unlocked {
		scope.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	scope.conn.Close()
	scope.conn = nil
}

func (scope *leaseTransactionScope) begin() (*sql.Tx, error) {
	return scope.store.beginLeaseTransactionOn(scope.conn)
}

// Lease reads are bounded by one subscription. On small tables, PostgreSQL can
// otherwise prefer sequential/bitmap scans that broaden Serializable predicate
// locks to unrelated subscriptions. Keep this choice local to lease accounting;
// reports and administration retain the normal planner settings.
func (s *Store) beginLeaseTransaction() (*sql.Tx, error) {
	return s.beginLeaseTransactionOn(nil)
}

func (s *Store) beginLeaseTransactionOn(conn *sql.Conn) (*sql.Tx, error) {
	var tx *sql.Tx
	var err error
	if conn != nil {
		tx, err = conn.BeginTx(context.Background(), nil)
	} else {
		tx, err = s.DB.Begin()
	}
	if err != nil {
		return nil, err
	}
	if s.Postgres() {
		for _, statement := range []string{"SET LOCAL enable_seqscan=off", "SET LOCAL enable_bitmapscan=off"} {
			if _, err = tx.Exec(statement); err != nil {
				tx.Rollback()
				return nil, err
			}
		}
	}
	return tx, nil
}

package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"time"
)

// WithMaintenanceLeadership holds a dedicated PostgreSQL session for the whole
// lifetime of background work. API replicas continue serving while waiting.
// Losing that session cancels work and fails the instance; it never reconnects
// and assumes that an old lock still belongs to it.
func (s *Store) WithMaintenanceLeadership(ctx context.Context, work func(context.Context) error) error {
	if !s.Postgres() {
		return work(ctx)
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(719022635,hashtext(current_schema()))").Scan(&acquired); err != nil {
			return err
		}
		if acquired {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var unlocked bool
		_ = conn.QueryRowContext(cleanup, "SELECT pg_advisory_unlock(719022635,hashtext(current_schema()))").Scan(&unlocked)
		// Even an interrupted unlock must never return a session holding a lock
		// to the general API pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}()
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- work(owned) }()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-tick.C:
			probe, stop := context.WithTimeout(ctx, time.Second)
			var alive int
			err := conn.QueryRowContext(probe, "SELECT 1").Scan(&alive)
			stop()
			if err != nil {
				cancel()
				<-done
				return errors.New("PostgreSQL 后台任务所有权连接中断，已停止任务")
			}
		}
	}
}

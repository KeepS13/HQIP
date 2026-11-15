package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const maintenanceBatchSize = 500

// Each table commits independently, at most one bounded batch per minute.
// A blocked table cannot roll back progress on other tables or monopolize the
// connection pool. Session rows remain for 30 days after refresh expiry.
func (s *Store) MaintenanceContext(parent context.Context) error {
	now := s.Now()
	var failures []error
	for _, q := range []struct {
		table, condition string
		cutoff           any
	}{
		{"nodes", "password_expires>0 AND password_expires<?", now.Unix()},
		{"sessions", "expires<?", now.Unix()},
		{"spent_refresh", "expires<?", now.Unix()},
		{"portal_spent_refresh", "expires<?", now.Unix()},
		{"portal_refresh_receipts", "expires<?", now.Unix()},
		{"portal_authorizations", "expires<?", now.Add(-24 * time.Hour).Unix()},
		{"portal_sessions", `expires<? AND NOT EXISTS(SELECT 1 FROM portal_spent_refresh r WHERE r.session_id=portal_sessions.id)
AND NOT EXISTS(SELECT 1 FROM portal_refresh_receipts r WHERE r.session_id=portal_sessions.id)
AND NOT EXISTS(SELECT 1 FROM portal_leases l WHERE l.session_id=portal_sessions.id AND l.closed=0)`, now.AddDate(0, 0, -30).Unix()},
		{"client_sessions", `refresh_expires<? AND NOT EXISTS(SELECT 1 FROM spent_refresh r WHERE r.session_id=client_sessions.id)
AND NOT EXISTS(SELECT 1 FROM removal_participants r WHERE r.session_id=client_sessions.id)`, now.AddDate(0, 0, -30).Unix()},
		{"minute", "start<?", now.AddDate(0, 0, -7).Unix()},
		{"hourly", "start<?", now.AddDate(0, 0, -90).Unix()},
		{"daily", "start<?", DayStart(now.AddDate(0, -13, 0))},
		{"events", "created_at<?", now.AddDate(0, 0, -30).Unix()},
		{"audit", "at<?", now.AddDate(0, -13, 0).Unix()},
		{"operation_logs", "last_at_ms<?", now.AddDate(0, -13, 0).UnixMilli()},
		{"operation_job_context", "created_at<?", now.AddDate(0, -13, 0).Unix()},
		{"jobs", "state IN ('done','error','cancelled') AND created_at<?", now.AddDate(0, 0, -30).Unix()},
		{"server_operations", "state IN ('succeeded','failed') AND finished_at<?", now.AddDate(0, 0, -90).Unix()},
	} {
		if parent.Err() != nil {
			return errors.Join(append(failures, parent.Err())...)
		}
		query := "DELETE FROM " + q.table
		if q.table == "nodes" {
			query = "UPDATE nodes SET password='',password_expires=0"
		}
		query += " WHERE rowid IN (SELECT rowid FROM " + q.table + " WHERE " + q.condition + " ORDER BY rowid LIMIT ?)"
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		_, err := s.DB.ExecContext(ctx, query, q.cutoff, maintenanceBatchSize)
		cancel()
		if err != nil {
			// Never emit database values or credentials through the worker log.
			failures = append(failures, fmt.Errorf("%s batch failed", q.table))
		}
	}
	return errors.Join(failures...)
}

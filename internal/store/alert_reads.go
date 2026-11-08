package store

import (
	"errors"

	"github.com/KeepS13/HQIP/internal/model"
)

// Read receipts share the alert's lifecycle and are pruned with its activities.
// Notes and acknowledgements do not make a previously read alert unread again.
const alertUnreadKinds = "'opened','escalated','unknown','resolved','ended'"

func (s *Store) AlertUnreadSummary() (model.AlertUnreadSummary, error) {
	var summary model.AlertUnreadSummary
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM alerts a WHERE a.status!='pending'
	 AND COALESCE((SELECT MAX(id) FROM alert_activity WHERE alert_id=a.id AND kind='read'),0)
	 <= COALESCE((SELECT MAX(id) FROM alert_activity WHERE alert_id=a.id AND kind IN (` + alertUnreadKinds + `)),0)`).Scan(&summary.Unread)
	return summary, err
}

// MarkAlertRead records only the revision actually displayed to the operator.
// It leaves both acknowledgement and the optimistic-write revision untouched.
func (s *Store) MarkAlertRead(id, actor string, expectedRevision int64) error {
	if expectedRevision < 1 {
		return errors.New("预警版本无效")
	}
	return retryDatabaseError(func() error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var revision int64
		query := "SELECT revision FROM alerts WHERE id=? AND status!='pending'"
		if s.Postgres() {
			// Activity IDs are allocated before commit. Lock the alert row so
			// an uncommitted evaluator change cannot be hidden by a newer read
			// ID created from an older snapshot. SERIALIZABLE retries refresh
			// that snapshot, then the revision check rejects stale details.
			query += " FOR UPDATE"
		}
		if err = tx.QueryRow(query, id).Scan(&revision); err != nil {
			return notFound(err)
		}
		if revision != expectedRevision {
			return ErrConflict
		}
		var lastRead, lastChange int64
		if err = tx.QueryRow(`SELECT COALESCE(MAX(CASE WHEN kind='read' THEN id ELSE 0 END),0),
		 COALESCE(MAX(CASE WHEN kind IN (`+alertUnreadKinds+`) THEN id ELSE 0 END),0)
		 FROM alert_activity WHERE alert_id=?`, id).Scan(&lastRead, &lastChange); err != nil {
			return err
		}
		if lastRead <= lastChange {
			if err = alertActivity(tx, id, "read", "管理员已阅读。", actor, s.Now().Unix()); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

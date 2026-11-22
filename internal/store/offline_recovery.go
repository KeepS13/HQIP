package store

import (
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
)

// RecoverOfflineGrant looks up an already committed admission using the node's
// durable request ID. It never issues or renews authority and deliberately works
// after credential rotation, revocation and expiry so an unused slot can settle.
// The authenticated node can only read its own grant. The ordinary subscription
// lock/transaction gives a coherent accounting receipt; missing requests return
// ErrNotFound, which does not prove an earlier admission cannot still commit.
func (s *Store) RecoverOfflineGrant(node, connection string) (model.OfflineGrant, error) {
	if len(connection) < 16 || len(connection) > 128 || strings.ContainsAny(connection, "\r\n\x00") {
		return model.OfflineGrant{}, ErrConflict
	}
	return retryDatabase(func() (model.OfflineGrant, error) {
		var empty model.OfflineGrant
		scope, err := s.leaseTransactionScope("SELECT subscription_id FROM portal_leases WHERE node_id=? AND connection_id=?", node, connection)
		if err != nil {
			return empty, err
		}
		defer scope.close()
		tx, err := scope.begin()
		if err != nil {
			return empty, err
		}
		defer tx.Rollback()
		var id string
		if err = tx.QueryRow("SELECT id FROM portal_leases WHERE node_id=? AND connection_id=?", node, connection).Scan(&id); err != nil {
			return empty, notFound(err)
		}
		return loadOfflineGrant(tx, node, id)
	})
}

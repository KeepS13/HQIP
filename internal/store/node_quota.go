package store

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

// AcquireNodeQuotaGrant opts into shared node quota; older callers retain
// reserved grants. Admission, slot limits and credentials use the same locked
// subscription transaction. No quota is multiplied by the number of parents.
func (s *Store) AcquireNodeQuotaGrant(node, connection, password, protocol string) (model.OfflineGrant, error) {
	if protocol != "hysteria2" && protocol != "vless" {
		return model.OfflineGrant{}, ErrConflict
	}
	l, err := s.acquireLease(node, connection, password, protocol, true, true)
	if err != nil {
		return model.OfflineGrant{}, err
	}
	return loadOfflineGrant(s.DB, node, l.ID)
}

// These counters are business acknowledgements, not operation logs. Every
// caller holds the subscription transaction scope; no independent transaction
// may increment a watermark before its corresponding usage has committed.
func quotaCounter(q portalQuery, key string) (int64, error) {
	var raw string
	err := q.QueryRow("SELECT value FROM metadata WHERE key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, ErrConflict
	}
	return value, nil
}

func addQuotaCounter(tx *sql.Tx, key string, delta int64) (int64, error) {
	value, err := quotaCounter(tx, key)
	if err != nil || delta < 0 {
		return 0, ErrConflict
	}
	value, err = billing.Add(value, delta)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, strconv.FormatInt(value, 10))
	return value, err
}

func recordNodeQuotaUsage(tx *sql.Tx, subscription, node string, delta int64) error {
	_, err := addQuotaCounter(tx, "node-quota-used:"+subscription+":"+node, delta)
	return err
}

func nextNodeQuotaSnapshot(tx *sql.Tx, e *model.UserSubscription, node string) (*model.NodeQuotaSnapshot, error) {
	q := &model.NodeQuotaSnapshot{}
	var err error
	q.Revision, err = addQuotaCounter(tx, "node-quota-revision:"+e.ID, 1)
	if err != nil {
		return nil, err
	}
	q.NodeUsedUnits, err = quotaCounter(tx, "node-quota-used:"+e.ID+":"+node)
	if err != nil {
		return nil, err
	}
	q.UsedUnits, err = billing.Units(e.UsedBytes, 100)
	if err != nil {
		return nil, err
	}
	var remainder int64
	err = tx.QueryRow("SELECT upload+download FROM traffic_billing_remainders WHERE subscription_id=?", e.ID).Scan(&remainder)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	q.UsedUnits, err = billing.Add(q.UsedUnits, remainder)
	if err != nil {
		return nil, err
	}
	q.LegacyReservedUnits, err = billing.Units(e.ReservedBytes, 100)
	if err != nil {
		return nil, err
	}
	if e.QuotaBytes != nil {
		units, err := billing.Units(*e.QuotaBytes, 100)
		if err != nil {
			return nil, err
		}
		q.QuotaUnits = &units
	}
	// A rollback must understand node-owned quota before serving these leases.
	_, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('node_quota_version','1') ON CONFLICT(key) DO NOTHING")
	return q, err
}

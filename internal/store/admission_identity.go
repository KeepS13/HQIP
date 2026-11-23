package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/KeepS13/HQIP/internal/secure"
)

// CheckAdmissionIdentity validates a transport's existing credential without
// reserving slots, bandwidth or bytes. It returns only the accounting identity,
// never a lease. Subsequent forwarding MUST use the ordinary admission
// transaction: this read-only snapshot is not a reservation or offline permit.
func (s *Store) CheckAdmissionIdentity(ctx context.Context, node, password string) (string, error) {
	if len(password) != 43 {
		return "", ErrUnauthorized
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var gate, hold string
	if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_enabled'").Scan(&gate); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_recovery_hold'").Scan(&hold); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if gate != "true" || hold == "true" {
		return "", ErrEntitlement
	}
	var user, subscription, scope string
	err = tx.QueryRow("SELECT user_id,subscription_id,scope FROM portal_credentials WHERE node_id=? AND password_hash=?", node, secure.Hash(password)).Scan(&user, &subscription, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	active, err := activeCredentialSession(tx, user, subscription, scope, s.Now())
	if err != nil {
		return "", err
	}
	if !active {
		return "", ErrUnauthorized
	}
	e, err := userSubscription(tx, user, subscription, s.Now())
	if err != nil {
		return "", err
	}
	if !entitlementAllows(e, node) {
		return "", ErrEntitlement
	}
	var usable int
	err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=? AND lifecycle='active' AND json_extract(config,'$.status')='ready'", node).Scan(&usable)
	if err != nil {
		return "", err
	}
	if usable != 1 {
		return "", ErrEntitlement
	}
	return "subscription_" + subscription, nil
}

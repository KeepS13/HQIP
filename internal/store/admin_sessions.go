package store

import (
	"time"

	"github.com/KeepS13/HQIP/internal/secure"
)

// ChangePasswordAndCreateSession rechecks the authenticated password and replaces
// all sessions atomically. A concurrent password reset cannot be overwritten by
// an earlier password check, or be followed by an unauthenticated new session.
func (s *Store) ChangePasswordAndCreateSession(expectedPasswordHash, password string) (token, csrf string, err error) {
	return s.adminSession(expectedPasswordHash, secure.HashPassword(password))
}

func (s *Store) adminSession(expectedPasswordHash, replacementHash string) (token, csrf string, err error) {
	if expectedPasswordHash == "" {
		return "", "", ErrUnauthorized
	}
	token, csrf = secure.Token(), secure.Token()
	expires := s.Now().Add(12 * time.Hour).Unix()
	err = retryDatabaseError(func() error {
		tx, e := s.DB.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		// A conditional write locks the admin row on both SQLite and PostgreSQL.
		// SELECT followed by INSERT alone could race with ChangePassword's DELETE.
		query := "UPDATE admin SET password=password WHERE id=1 AND password=?"
		args := []any{expectedPasswordHash}
		if replacementHash != "" {
			query = "UPDATE admin SET password=?,must_change=0 WHERE id=1 AND password=?"
			args = []any{replacementHash, expectedPasswordHash}
		}
		result, e := tx.Exec(query, args...)
		if e != nil {
			return e
		}
		changed, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if changed != 1 {
			return ErrUnauthorized
		}
		if replacementHash != "" {
			if _, e = tx.Exec("DELETE FROM sessions"); e != nil {
				return e
			}
		}
		if _, e = tx.Exec("INSERT INTO sessions(hash,csrf,expires) VALUES(?,?,?)", secure.Hash(token), csrf, expires); e != nil {
			return e
		}
		return tx.Commit()
	})
	if err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

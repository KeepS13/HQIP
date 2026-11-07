package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDatabaseRetryHandlesContentionButNotAmbiguousFailures(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		attempts := 0
		value, err := retryDatabase(func() (int, error) {
			attempts++
			if attempts <= 10 {
				return 0, fmt.Errorf("transaction: %w", &pgconn.PgError{Code: code})
			}
			return 42, nil
		})
		if err != nil || value != 42 || attempts != 11 {
			t.Fatal("serializable contention was not retried", code, attempts, value, err)
		}
	}
	for _, failure := range []error{errors.New("connection lost after COMMIT"), ErrConflict, &pgconn.PgError{Code: "23505"}, &pgconn.PgError{Code: "57014"}} {
		attempts := 0
		_, err := retryDatabase(func() (int, error) {
			attempts++
			return 0, failure
		})
		if attempts != 1 || !errors.Is(err, failure) {
			t.Fatal("unsafe or business failure was retried", attempts, err)
		}
	}
}

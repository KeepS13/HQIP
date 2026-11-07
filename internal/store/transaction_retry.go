package store

import (
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Only retry transactions PostgreSQL guarantees did not commit. Retrying an
// ambiguous network failure could duplicate effects, so those errors propagate.
func retryDatabase[T any](attempt func() (T, error)) (T, error) {
	var result T
	var err error
	for i := 0; i < 6; i++ {
		result, err = attempt()
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || (pg.Code != "40001" && pg.Code != "40P01") {
			return result, err
		}
		if i < 5 {
			time.Sleep(time.Duration(5*(1<<i)+rand.IntN(10)) * time.Millisecond)
		}
	}
	return result, err
}

func retryDatabaseError(attempt func() error) error {
	_, err := retryDatabase(func() (struct{}, error) { return struct{}{}, attempt() })
	return err
}

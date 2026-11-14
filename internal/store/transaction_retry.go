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
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; i < 32; i++ {
		result, err = attempt()
		var pg *pgconn.PgError
		var sqlite interface{ Code() int }
		busy := errors.As(err, &sqlite) && (sqlite.Code()&255 == 5 || sqlite.Code()&255 == 6)
		if !busy && (!errors.As(err, &pg) || (pg.Code != "40001" && pg.Code != "40P01")) {
			return result, err
		}
		// A short fixed attempt count can exhaust while a burst of Agent
		// replays is still serializing. Bound elapsed retry time and cap each
		// backoff instead; full jitter prevents contenders moving in lockstep.
		remaining := time.Until(deadline)
		if i == 31 || remaining <= 0 {
			return result, err
		}
		ceiling := min(time.Duration(5*(1<<min(i, 6)))*time.Millisecond, 200*time.Millisecond, remaining)
		time.Sleep(time.Duration(rand.Int64N(int64(ceiling)) + 1))
		if !time.Now().Before(deadline) {
			return result, err
		}
	}
	return result, err
}

func retryDatabaseError(attempt func() error) error {
	_, err := retryDatabase(func() (struct{}, error) { return struct{}{}, attempt() })
	return err
}

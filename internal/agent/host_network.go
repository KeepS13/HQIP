package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/KeepS13/HQIP/internal/hostnetwork"
	"github.com/KeepS13/HQIP/internal/model"
)

const networkPendingPrefix = "host-network:pending:"

// sampleHostNetwork runs even if both proxy protocols are unhealthy. Persistence
// uses the existing FULL-synchronous ledger; network failure never discards an
// unacknowledged boot. l.mu precedes SQLite, as in all other local ledger writes.
func (a *Agent) sampleHostNetwork(ctx context.Context) error {
	sample, err := hostnetwork.Read(a.now())
	if errors.Is(err, hostnetwork.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.ledger.recordHostNetwork(ctx, sample)
}

func (l *ledger) recordHostNetwork(ctx context.Context, sample model.HostNetworkSample) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	var previous model.HostNetworkSample
	err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='host-network:current'").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &previous); err != nil {
			return err
		}
		if !sample.At.After(previous.At) {
			return errors.New("host network clock regressed")
		}
		// Preserve the last pre-reset value so an offline counter reset cannot
		// overwrite the only durable record of traffic in the old incarnation.
		if previous.BootID == sample.BootID && networkCounterReset(previous, sample) {
			key := networkPendingPrefix + previous.BootID + ":" + fmt.Sprint(previous.At.UnixNano())
			if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", key, raw); err != nil {
				return err
			}
		}
	}
	encoded, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	for _, key := range []string{"host-network:current", networkPendingPrefix + sample.BootID} {
		if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(encoded)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func networkCounterReset(old, next model.HostNetworkSample) bool {
	for _, before := range old.Interfaces {
		found := false
		for _, after := range next.Interfaces {
			if before.Index == after.Index {
				found = true
				if after.Receive < before.Receive || after.Send < before.Send {
					return true
				}
			}
		}
		// Removing a carrier must not overwrite its last unacknowledged total.
		if !found {
			return true
		}
	}
	return false
}

// pendingHostNetwork keeps requests bounded. Samples use UTC; pad the optional
// RFC3339 fractional part to nanoseconds, so whole seconds sort before fractions.
// A pre-reset checkpoint must precede the same boot's new counters on replay.
func (l *ledger) pendingHostNetwork() (map[string]string, []model.HostNetworkSample, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, err := l.db.Query(`SELECT key,value FROM meta WHERE key LIKE 'host-network:pending:%'
		ORDER BY substr(json_extract(value,'$.at'),1,19),
		substr(rtrim(substr(json_extract(value,'$.at'),21),'Z') || '000000000',1,9) LIMIT 8`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	batch := map[string]string{}
	var samples []model.HostNetworkSample
	for rows.Next() {
		var key, raw string
		var sample model.HostNetworkSample
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, nil, err
		}
		if err = json.Unmarshal([]byte(raw), &sample); err != nil {
			return nil, nil, err
		}
		batch[key], samples = raw, append(samples, sample)
	}
	return batch, samples, rows.Err()
}

func (l *ledger) acknowledgeHostNetwork(batch map[string]string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, raw := range batch {
		// A concurrent sample may already have replaced the value sent over WAN.
		if _, err = tx.Exec("DELETE FROM meta WHERE key=? AND value=?", key, raw); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// networkSampleDue is intentionally local to the sampling goroutine; neither
// control-plane latency nor HY2 health controls the five-second host schedule.
func networkSampleDue(now, previous time.Time) bool {
	return previous.IsZero() || now.Sub(previous) >= 5*time.Second
}

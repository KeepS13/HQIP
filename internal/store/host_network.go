package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

const hostNetworkTotalPrefix = "host-network:total:"

var networkBootID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type networkBoot struct {
	At       time.Time                          `json:"at"`
	Counters map[int]model.HostNetworkInterface `json:"counters"`
	Active   []int                              `json:"active"`
}

func validateHostNetwork(samples []model.HostNetworkSample, now time.Time) error {
	if len(samples) > 8 {
		return errors.New("too many host network samples")
	}
	for _, s := range samples {
		if !networkBootID.MatchString(s.BootID) || s.BootAt.Unix() <= 0 || s.At.Before(s.BootAt) || s.At.After(now.Add(5*time.Minute)) || len(s.Interfaces) == 0 || len(s.Interfaces) > 64 || len(s.Excluded) > 256 {
			return errors.New("invalid host network sample")
		}
		seen := map[int]bool{}
		for _, n := range s.Interfaces {
			if n.Index <= 0 || seen[n.Index] || n.Name == "" || len(n.Name) > 64 || strings.ContainsAny(n.Name, "/\\\x00\r\n") || n.Receive < 0 || n.Send < 0 || n.Receive > 1<<60 || n.Send > 1<<60 {
				return errors.New("invalid host interface counters")
			}
			seen[n.Index] = true
		}
		for _, name := range s.Excluded {
			if len(name) > 64 {
				return errors.New("invalid excluded interface")
			}
		}
	}
	return nil
}

// recordHostNetwork shares the agent report transaction, but never writes the
// subscription/proxy ledgers. The server-scoped metadata row is the lock before
// per-boot rows: all agents on one host merge the same OS counters exactly once.
// These rows are business counter state, not provenance or operational evidence.
func recordHostNetwork(ctx context.Context, tx *sql.Tx, node string, samples []model.HostNetworkSample, now time.Time) error {
	if len(samples) == 0 {
		return nil
	}
	var server string
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(json_extract(config,'$.serverId'),'') FROM nodes WHERE id=?", node).Scan(&server); err != nil {
		return err
	}
	if server == "" {
		return errors.New("host network sample requires a registered server")
	}
	key := hostNetworkTotalPrefix + server
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,'{}') ON CONFLICT(key) DO NOTHING", key); err != nil {
		return err
	}
	// UPDATE provides the PostgreSQL row lock too; revision/business config is
	// untouched. SQLite already serializes writers. Retry rolls back all counters.
	if _, err := tx.ExecContext(ctx, "UPDATE metadata SET value=value WHERE key=?", key); err != nil {
		return err
	}
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&raw); err != nil {
		return err
	}
	var total model.HostNetworkUsage
	if err := json.Unmarshal([]byte(raw), &total); err != nil {
		return err
	}
	total.ServerID = server
	if total.FirstObserved.IsZero() {
		total.FirstObserved = now.UTC()
	}
	for _, sample := range samples {
		if err := mergeHostNetwork(ctx, tx, server, &total, sample); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE metadata SET value=? WHERE key=?", jsonString(total), key)
	return err
}

func mergeHostNetwork(ctx context.Context, tx *sql.Tx, server string, total *model.HostNetworkUsage, sample model.HostNetworkSample) error {
	key := "host-network:boot:" + server + ":" + sample.BootID
	state := networkBoot{Counters: map[int]model.HostNetworkInterface{}}
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			return err
		}
	}
	if !sample.At.After(state.At) {
		return nil
	} // Replay/late other-agent report.
	if state.Counters == nil {
		state.Counters = map[int]model.HostNetworkInterface{}
	}
	var receive, send int64
	changed := false
	active := make([]int, 0, len(sample.Interfaces))
	for _, counter := range sample.Interfaces {
		active = append(active, counter.Index)
		previous, exists := state.Counters[counter.Index]
		rx, tx := counter.Receive-previous.Receive, counter.Send-previous.Send
		if rx < 0 {
			rx, changed = counter.Receive, true
		}
		if tx < 0 {
			tx, changed = counter.Send, true
		}
		if !exists && !state.At.IsZero() {
			changed = true
		}
		if receive > 1<<60-rx || send > 1<<60-tx {
			return errors.New("host traffic overflow")
		}
		receive, send = receive+rx, send+tx
		state.Counters[counter.Index] = counter
	}
	if len(state.Counters) > 256 {
		return errors.New("too many host NIC incarnations")
	}
	// Historical watermarks remain for reappearing interfaces. Only a change in
	// the active set invalidates this rate interval, not every later interval.
	slices.Sort(active)
	if !state.At.IsZero() && !slices.Equal(active, state.Active) {
		changed = true
	}
	state.Active = active
	if total.Receive > 1<<60-receive || total.Send > 1<<60-send {
		return errors.New("host total overflow")
	}
	total.Receive, total.Send = total.Receive+receive, total.Send+send
	if sample.At.After(total.Sample.At) {
		seconds := sample.At.Sub(total.Sample.At).Seconds()
		sameBoot := sample.BootID == total.Sample.BootID
		total.ReceiveRate, total.SendRate = nil, nil
		if sameBoot && seconds > 0 && seconds <= 30 && !changed {
			rx, tx := float64(receive)/seconds, float64(send)/seconds
			total.ReceiveRate, total.SendRate = &rx, &tx
		}
		if !total.Sample.At.IsZero() && !sameBoot {
			total.Discontinuity = true
		}
		total.Sample = sample
	}
	total.Discontinuity = total.Discontinuity || changed
	state.At = sample.At
	_, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, jsonString(state))
	return err
}

func decodeHostNetwork(raw string, now time.Time) (*model.HostNetworkUsage, error) {
	var value model.HostNetworkUsage
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	age := now.Sub(value.Sample.At)
	value.Fresh = age >= 0 && age <= 30*time.Second
	if !value.Fresh {
		value.ReceiveRate, value.SendRate = nil, nil
	}
	return &value, nil
}

// HostNetwork returns nil for an older agent; absence is never measured zero.
func (s *Store) HostNetwork(server string) (*model.HostNetworkUsage, error) {
	var raw string
	err := s.DB.QueryRow("SELECT value FROM metadata WHERE key=?", hostNetworkTotalPrefix+server).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeHostNetwork(raw, s.Now())
}

// HostNetworks is one batch query for the administrator node list, preventing a
// new per-node query and preventing callers from summing copies of the same host.
func (s *Store) HostNetworks() (map[string]*model.HostNetworkUsage, error) {
	rows, err := s.DB.Query("SELECT value FROM metadata WHERE key LIKE 'host-network:total:%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*model.HostNetworkUsage{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		value, err := decodeHostNetwork(raw, s.Now())
		if err != nil {
			return nil, err
		}
		out[value.ServerID] = value
	}
	return out, rows.Err()
}

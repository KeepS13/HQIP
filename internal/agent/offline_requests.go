package agent

import (
	"database/sql"
	"errors"
)

type offlineRequest struct {
	Hash string
	ID   string
}

// offlineMu serializes the caller. The ledger lock covers SQLite only, never a
// WAN request. A request ID must survive every uncertain dispatch outcome.
func (l *ledger) trackOfflineRequest(hash, connection string) (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var id string
	err := l.db.QueryRow("SELECT request_id FROM offline_requests WHERE hash=?", hash).Scan(&id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return id, false, err
	}
	var count int
	if err = l.db.QueryRow("SELECT COUNT(*) FROM offline_requests").Scan(&count); err != nil {
		return "", false, err
	}
	if count >= maxLeaseQueueRows {
		return "", false, errors.New("offline request capacity exceeded")
	}
	_, err = l.db.Exec("INSERT INTO offline_requests(hash,request_id) VALUES(?,?)", hash, connection)
	return connection, true, err
}

func (l *ledger) forgetOfflineRequest(r offlineRequest) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.db.Exec("DELETE FROM offline_requests WHERE hash=? AND request_id=?", r.Hash, r.ID)
	return err
}

// A bounded, ordered batch supports round-robin recovery. A missing control
// receipt is retained: its original request may still be committing remotely.
func (l *ledger) offlineRequests(after string) ([]offlineRequest, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, err := l.db.Query("SELECT hash,request_id FROM offline_requests WHERE hash>? ORDER BY hash LIMIT 16", after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []offlineRequest
	for rows.Next() {
		var r offlineRequest
		if err = rows.Scan(&r.Hash, &r.ID); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

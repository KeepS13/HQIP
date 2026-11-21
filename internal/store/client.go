package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Read-only inspection of historical device accounts for ledger maintenance.
func (s *Store) ClientAccount(deviceID string) (model.ClientAccount, error) {
	var result model.ClientAccount
	err := s.DB.QueryRow("SELECT username FROM device_accounts WHERE device_id=?", deviceID).Scan(&result.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	result.Initialized = err == nil
	return result, err
}
func (s *Store) ClientSessions(deviceID string) ([]model.ClientSession, error) {
	a, err := s.Assignment(deviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query("SELECT id,installation_id,client_name,last_seen,status FROM client_sessions WHERE device_id=? AND revoked=0 AND refresh_expires>? ORDER BY last_seen DESC", deviceID, s.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.ClientSession{}
	for rows.Next() {
		var c model.ClientSession
		var raw string
		var last int64
		if err = rows.Scan(&c.ID, &c.InstallationID, &c.ClientName, &last, &raw); err != nil {
			return nil, err
		}
		var status model.ClientStatus
		_ = json.Unmarshal([]byte(raw), &status)
		c.CoreVersion = status.CoreVersion
		c.DesiredNodeID = status.DesiredNodeID
		c.ActualNodeID = status.ActualNodeID
		c.RunState = status.RunState
		c.Mode = status.Mode
		c.TUNEnabled = status.TUNEnabled
		c.ConnectionIntent = status.ConnectionIntent
		c.AppliedConfigRevision = status.AppliedConfigRevision
		c.ProcessedConfigRevision = status.ProcessedConfigRevision
		c.DesiredConfigRevision = a.Revision
		c.RevocationPending = status.AppliedConfigRevision < a.Revision
		c.LastError = status.LastError
		t := time.Unix(last, 0).UTC()
		c.LastSeen = &t
		result = append(result, c)
	}
	return result, rows.Err()
}

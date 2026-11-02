package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func (s *Store) CreateDevice(name string) (model.Device, error) {
	d := model.Device{ID: NewID(), Name: name, Enabled: true, CreatedAt: s.Now().UTC()}
	token := secure.Token()
	enc, err := s.Vault.Encrypt(token)
	if err != nil {
		return d, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO devices(id,name,created_at,token_hash,token_secret) VALUES(?,?,?,?,?)", d.ID, name, s.Now().Unix(), secure.Hash(token), enc); err != nil {
		return d, err
	}
	if _, err = tx.Exec("INSERT INTO device_config(device_id) VALUES(?)", d.ID); err != nil {
		return d, err
	}
	if err = tx.Commit(); err != nil {
		return d, err
	}
	s.Audit("device.create", d.ID)
	return s.Device(d.ID)
}
func nodeIDs(tx *sql.Tx) ([]string, error) {
	rows, err := tx.Query("SELECT id FROM nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) Device(id string) (model.Device, error) {
	var d model.Device
	var created int64
	var enc string
	err := s.DB.QueryRow("SELECT id,name,enabled,created_at,token_secret FROM devices WHERE id=?", id).Scan(&d.ID, &d.Name, &d.Enabled, &created, &enc)
	if err != nil {
		return d, notFound(err)
	}
	d.CreatedAt = time.Unix(created, 0).UTC()
	token, err := s.Vault.Decrypt(enc)
	if err != nil {
		return d, secretError(err)
	}
	d.SubscriptionURL = s.BaseURL + "/sub/" + token
	err = s.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM daily WHERE device_id=? AND start>=?", id, MonthStart(s.Now())).Scan(&d.MonthUpload, &d.MonthDownload)
	if err != nil {
		return d, err
	}
	err = s.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM lifetime WHERE device_id=?", id).Scan(&d.TotalUpload, &d.TotalDownload)
	if err != nil {
		return d, err
	}
	rows, err := s.DB.Query("SELECT n.last_seen,n.online,n.desired_revision,n.applied_revision,n.core_healthy FROM nodes n JOIN credentials c ON c.node_id=n.id WHERE c.device_id=?", id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var last, desired, applied int64
		var raw string
		var healthy bool
		if err = rows.Scan(&last, &raw, &desired, &applied, &healthy); err != nil {
			return d, err
		}
		if desired > applied {
			d.PendingSync = true
		}
		if healthy && last > s.Now().Add(-30*time.Second).Unix() {
			var online map[string]int
			_ = json.Unmarshal([]byte(raw), &online)
			if online[id] > 0 {
				d.Online = true
				d.Instances += online[id]
			}
		}
	}
	if err = rows.Err(); err != nil {
		return d, err
	}
	rows.Close()
	var pending int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM removals WHERE device_id=? AND status NOT IN ('revoked','cancelled')", id).Scan(&pending); err != nil {
		return d, err
	}
	if pending > 0 {
		d.PendingSync = true
	}
	return d, nil
}
func (s *Store) Devices() ([]model.Device, error) {
	rows, err := s.DB.Query("SELECT id FROM devices ORDER BY created_at DESC,id")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []model.Device{}
	for _, id := range ids {
		d, e := s.Device(id)
		if e != nil {
			return nil, e
		}
		result = append(result, d)
	}
	return result, nil
}

// NodeDevices retains the same response type but narrows usage and connection
// counts to the selected node, matching the node detail page's table heading.
func (s *Store) NodeDevices(nodeID string) ([]model.Device, error) {
	n, err := s.Node(nodeID)
	if err != nil {
		return nil, err
	}
	devices, err := s.Devices()
	if err != nil {
		return nil, err
	}
	var raw string
	if err = s.DB.QueryRow("SELECT online FROM nodes WHERE id=?", nodeID).Scan(&raw); err != nil {
		return nil, err
	}
	online := map[string]int{}
	_ = json.Unmarshal([]byte(raw), &online)
	fresh := n.LastSeen != nil && s.Now().Sub(*n.LastSeen) < 30*time.Second && n.CoreHealthy
	for i := range devices {
		d := &devices[i]
		if err = s.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM daily WHERE node_id=? AND device_id=? AND start>=?", nodeID, d.ID, MonthStart(s.Now())).Scan(&d.MonthUpload, &d.MonthDownload); err != nil {
			return nil, err
		}
		if err = s.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM lifetime WHERE node_id=? AND device_id=?", nodeID, d.ID).Scan(&d.TotalUpload, &d.TotalDownload); err != nil {
			return nil, err
		}
		d.Online = fresh && online[d.ID] > 0
		d.Instances = 0
		if fresh {
			d.Instances = online[d.ID]
		}
		d.PendingSync = n.DesiredRevision > n.AppliedRevision
	}
	return devices, nil
}
func (s *Store) UpdateDevice(id string, name *string, enabled *bool, rotate bool) (model.Device, error) {
	d, err := s.Device(id)
	if err != nil {
		return d, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	if name != nil {
		if _, err = tx.Exec("UPDATE devices SET name=? WHERE id=?", *name, id); err != nil {
			return d, err
		}
	}
	changed := enabled != nil && *enabled != d.Enabled
	if enabled != nil {
		if _, err = tx.Exec("UPDATE devices SET enabled=? WHERE id=?", *enabled, id); err != nil {
			return d, err
		}
	}
	if rotate {
		token := secure.Token()
		enc, e := s.Vault.Encrypt(token)
		if e != nil {
			return d, e
		}
		if _, err = tx.Exec("UPDATE devices SET token_hash=?,token_secret=? WHERE id=?", secure.Hash(token), enc, id); err != nil {
			return d, err
		}
		ids, e := nodeIDs(tx)
		if e != nil {
			return d, e
		}
		for _, nid := range ids {
			enc, e = s.Vault.Encrypt(secure.Token())
			if e != nil {
				return d, e
			}
			if _, err = tx.Exec("UPDATE credentials SET password=? WHERE node_id=? AND device_id=?", enc, nid, id); err != nil {
				return d, err
			}
		}
	}
	if rotate || changed {
		if _, err = tx.Exec("UPDATE nodes SET desired_revision=desired_revision+1"); err != nil {
			return d, err
		}
		if _, err = tx.Exec("UPDATE device_config SET revision=revision+1 WHERE device_id=?", id); err != nil {
			return d, err
		}
		if _, err = tx.Exec("UPDATE assignments SET required_revision=(SELECT desired_revision FROM nodes WHERE nodes.id=assignments.node_id) WHERE device_id=?", id); err != nil {
			return d, err
		}
		if enabled != nil && !*enabled {
			if _, err = tx.Exec("UPDATE client_sessions SET revoked=1 WHERE device_id=?", id); err != nil {
				return d, err
			}
			if _, err = tx.Exec("UPDATE removals SET immediate=1 WHERE device_id=? AND status NOT IN ('revoked','cancelled')", id); err != nil {
				return d, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return d, err
	}
	action := "device.update"
	if rotate {
		action = "device.rotate"
	}
	s.Audit(action, id)
	return s.Device(id)
}
func (s *Store) SubscriptionDevice(token string) (model.Device, error) {
	var id string
	err := s.DB.QueryRow("SELECT id FROM devices WHERE token_hash=? AND enabled=1", secure.Hash(token)).Scan(&id)
	if err != nil {
		return model.Device{}, notFound(err)
	}
	return s.Device(id)
}
func (s *Store) Credential(nodeID, deviceID string) (string, error) {
	var enc string
	err := s.DB.QueryRow("SELECT password FROM credentials WHERE node_id=? AND device_id=?", nodeID, deviceID).Scan(&enc)
	if err != nil {
		return "", notFound(err)
	}
	pass, err := s.Vault.Decrypt(enc)
	return pass, secretError(err)
}

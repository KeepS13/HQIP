package store

import (
	"database/sql"
	"encoding/json"
	"github.com/KeepS13/HQIP/internal/model"
)

// Version three preserves node/device identifiers and all accounting keys.
// Only positively pinned hosts share a server; unverified installations remain
// separate until an administrator verifies their physical identity.
func migrateServers(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE servers(id TEXT PRIMARY KEY,config TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1,deleted_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE server_addresses(id TEXT PRIMARY KEY,server_id TEXT NOT NULL REFERENCES servers(id),ip TEXT NOT NULL,created_at INTEGER NOT NULL,deleted_at INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX active_server_address ON server_addresses(server_id,ip) WHERE deleted_at=0;
ALTER TABLE nodes ADD COLUMN deleted_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active';
ALTER TABLE nodes ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN managed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN capabilities TEXT NOT NULL DEFAULT '[]';
ALTER TABLE nodes ADD COLUMN certificate_status TEXT NOT NULL DEFAULT '{}';
ALTER TABLE devices ADD COLUMN deleted_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN options TEXT NOT NULL DEFAULT '{}';
CREATE TABLE node_retirements(node_id TEXT PRIMARY KEY REFERENCES nodes(id),id TEXT NOT NULL UNIQUE,stopped INTEGER NOT NULL DEFAULT 0,final INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);
CREATE TABLE node_preflights(node_id TEXT PRIMARY KEY REFERENCES nodes(id),fingerprint TEXT NOT NULL,takeover INTEGER NOT NULL,created_at INTEGER NOT NULL);
`)
	if err != nil {
		return err
	}
	rows, err := tx.Query("SELECT id,config FROM nodes ORDER BY id")
	if err != nil {
		return err
	}
	nodes := []model.Node{}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var n model.Node
		if err = json.Unmarshal([]byte(raw), &n); err != nil {
			rows.Close()
			return err
		}
		n.ID = id
		nodes = append(nodes, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	pinned := map[string]string{}
	for _, n := range nodes {
		serverID := ""
		identity := n.HostKey + ":" + n.MachineID
		if n.HostKey != "" && n.MachineID != "" {
			serverID = pinned[identity]
		}
		if serverID == "" {
			serverID = NewID()
			srv := model.Server{ID: serverID, Name: n.Name, SSHHost: n.SSHHost, SSHPort: n.SSHPort, SSHUser: n.SSHUser, HostKey: n.HostKey, MachineID: n.MachineID, Revision: 1, CreatedAt: n.CreatedAt}
			if _, err = tx.Exec("INSERT INTO servers(id,config) VALUES(?,?)", serverID, jsonString(srv)); err != nil {
				return err
			}
			if n.HostKey != "" && n.MachineID != "" {
				pinned[identity] = serverID
			}
		}
		var addressID string
		err = tx.QueryRow("SELECT id FROM server_addresses WHERE server_id=? AND ip=?", serverID, n.PublicIP).Scan(&addressID)
		if err == sql.ErrNoRows {
			addressID = NewID()
			_, err = tx.Exec("INSERT INTO server_addresses(id,server_id,ip,created_at) VALUES(?,?,?,?)", addressID, serverID, n.PublicIP, n.CreatedAt.Unix())
		}
		if err != nil {
			return err
		}
		n.ServerID = serverID
		n.AddressID = addressID
		n.AuthPort = 18081
		n.StatsPort = 18082
		n.InstallationVersion = 1
		n.CertificateMode = "http"
		n.DNSMode = "manual"
		n.Lifecycle = "active"
		n.Revision = 1
		// A successful heartbeat proves that this controller owns the installed
		// agent. A failed owner-conflict attempt must never imply ownership.
		var seen int64
		if err = tx.QueryRow("SELECT last_seen FROM nodes WHERE id=?", n.ID).Scan(&seen); err != nil {
			return err
		}
		n.Managed = seen > 0 || n.Status == "ready" || n.Status == "awaiting_external"
		if _, err = tx.Exec("UPDATE nodes SET config=?,managed=? WHERE id=?", jsonString(n), n.Managed, n.ID); err != nil {
			return err
		}
	}
	_, err = tx.Exec("INSERT OR IGNORE INTO metadata(key,value) VALUES('controller_id',?); UPDATE metadata SET value='3' WHERE key='schema_version'", NewID())
	return err
}

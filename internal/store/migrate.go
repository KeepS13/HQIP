package store

import (
	"database/sql"
	"errors"
)

// Version two snapshots existing grants; future nodes are never implicitly
// added to a device. Proxy secrets and lifetime counter keys remain unchanged.
func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version string
	if err = tx.QueryRow("SELECT value FROM metadata WHERE key='schema_version'").Scan(&version); err != nil {
		return err
	}
	if version == "2" {
		return tx.Commit()
	}
	if version != "1" {
		return errors.New("数据库版本不受此程序支持")
	}
	_, err = tx.Exec(`
CREATE TABLE device_config(device_id TEXT PRIMARY KEY REFERENCES devices(id),default_node_id TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE assignments(device_id TEXT NOT NULL REFERENCES devices(id),node_id TEXT NOT NULL REFERENCES nodes(id),required_revision INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(device_id,node_id));
CREATE TABLE device_accounts(device_id TEXT PRIMARY KEY REFERENCES devices(id),username TEXT NOT NULL UNIQUE COLLATE NOCASE,password TEXT NOT NULL);
CREATE TABLE client_sessions(id TEXT PRIMARY KEY,device_id TEXT NOT NULL REFERENCES devices(id),installation_id TEXT NOT NULL,client_name TEXT NOT NULL,access_hash TEXT NOT NULL UNIQUE,refresh_hash TEXT NOT NULL UNIQUE,access_expires INTEGER NOT NULL,refresh_expires INTEGER NOT NULL,last_seen INTEGER NOT NULL,created_at INTEGER NOT NULL,status TEXT NOT NULL DEFAULT '{}',revoked INTEGER NOT NULL DEFAULT 0);
CREATE INDEX client_sessions_device ON client_sessions(device_id);
CREATE TABLE spent_refresh(hash TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES client_sessions(id),expires INTEGER NOT NULL);
CREATE TABLE removals(id TEXT PRIMARY KEY,device_id TEXT NOT NULL REFERENCES devices(id),node_id TEXT NOT NULL REFERENCES nodes(id),revision INTEGER NOT NULL,created_at INTEGER NOT NULL,status TEXT NOT NULL DEFAULT 'pending',deadline INTEGER NOT NULL DEFAULT 0,immediate INTEGER NOT NULL DEFAULT 0);
CREATE INDEX removals_device ON removals(device_id,node_id);
CREATE TABLE removal_participants(removal_id TEXT NOT NULL REFERENCES removals(id),session_id TEXT NOT NULL REFERENCES client_sessions(id),PRIMARY KEY(removal_id,session_id));
CREATE TABLE rule_versions(hash TEXT PRIMARY KEY,version INTEGER NOT NULL UNIQUE,body BLOB NOT NULL,created_at INTEGER NOT NULL);
INSERT INTO device_config(device_id,default_node_id) SELECT d.id,COALESCE((SELECT c.node_id FROM credentials c JOIN nodes n ON n.id=c.node_id WHERE c.device_id=d.id ORDER BY CASE WHEN json_extract(n.config,'$.status')='ready' THEN 0 ELSE 1 END,c.node_id LIMIT 1),'') FROM devices d;
INSERT INTO assignments(device_id,node_id,required_revision) SELECT c.device_id,c.node_id,n.desired_revision FROM credentials c JOIN devices d ON d.id=c.device_id JOIN nodes n ON n.id=c.node_id;
UPDATE metadata SET value='2' WHERE key='schema_version';
`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

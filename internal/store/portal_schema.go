package store

import "database/sql"

func migratePortal(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS portal_plans(id TEXT PRIMARY KEY,config TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS portal_users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE COLLATE NOCASE,password TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,created_at INTEGER NOT NULL,plan_id TEXT NOT NULL DEFAULT '',expires INTEGER NOT NULL DEFAULT 0,epoch INTEGER NOT NULL DEFAULT 1,revision INTEGER NOT NULL DEFAULT 1,subscription_hash TEXT NOT NULL DEFAULT '',subscription_secret TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS portal_subscriptions ON portal_users(subscription_hash) WHERE subscription_hash!='';
CREATE TABLE IF NOT EXISTS portal_usage(user_id TEXT NOT NULL REFERENCES portal_users(id),epoch INTEGER NOT NULL,upload INTEGER NOT NULL DEFAULT 0,download INTEGER NOT NULL DEFAULT 0,reserved INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(user_id,epoch));
CREATE TABLE IF NOT EXISTS portal_credentials(user_id TEXT NOT NULL REFERENCES portal_users(id),node_id TEXT NOT NULL REFERENCES nodes(id),scope TEXT NOT NULL,password_hash TEXT NOT NULL UNIQUE,password_secret TEXT NOT NULL,PRIMARY KEY(user_id,node_id,scope));
CREATE TABLE IF NOT EXISTS portal_sessions(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES portal_users(id),kind TEXT NOT NULL,client_name TEXT NOT NULL,installation_id TEXT NOT NULL DEFAULT '',access_hash TEXT NOT NULL UNIQUE,refresh_hash TEXT NOT NULL UNIQUE,csrf TEXT NOT NULL,access_expires INTEGER NOT NULL,expires INTEGER NOT NULL,created_at INTEGER NOT NULL,last_seen INTEGER NOT NULL,revoked INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS portal_spent_refresh(hash TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES portal_sessions(id),expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS portal_authorizations(id TEXT PRIMARY KEY,challenge TEXT NOT NULL,redirect_uri TEXT NOT NULL,state TEXT NOT NULL,installation_id TEXT NOT NULL,client_name TEXT NOT NULL,expires INTEGER NOT NULL,user_id TEXT NOT NULL DEFAULT '',code_hash TEXT NOT NULL DEFAULT '',consumed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS portal_leases(id TEXT PRIMARY KEY,node_id TEXT NOT NULL REFERENCES nodes(id),connection_id TEXT NOT NULL,user_id TEXT NOT NULL REFERENCES portal_users(id),session_id TEXT NOT NULL,epoch INTEGER NOT NULL,revision INTEGER NOT NULL,created_at INTEGER NOT NULL,expires INTEGER NOT NULL,bandwidth INTEGER NOT NULL,budget INTEGER NOT NULL,upload INTEGER NOT NULL DEFAULT 0,download INTEGER NOT NULL DEFAULT 0,closed INTEGER NOT NULL DEFAULT 0,revoked INTEGER NOT NULL DEFAULT 0,UNIQUE(node_id,connection_id));
CREATE INDEX IF NOT EXISTS portal_lease_user ON portal_leases(user_id,expires,closed);
CREATE TABLE IF NOT EXISTS portal_rates(lease_id TEXT PRIMARY KEY REFERENCES portal_leases(id),version INTEGER NOT NULL DEFAULT 1,ack INTEGER NOT NULL DEFAULT 0,ceiling INTEGER NOT NULL DEFAULT 0,pending INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS portal_turnstile_tokens(hash TEXT PRIMARY KEY,expires INTEGER NOT NULL);
INSERT OR IGNORE INTO metadata(key,value) VALUES('portal_enabled','false'),('portal_registration','false');
UPDATE metadata SET value='4' WHERE key='schema_version';
`)
	return err
}

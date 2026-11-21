package store

import (
	"database/sql"
	"errors"
)

// Identities authorize telemetry ingestion only, never connection admission.
const accountingSchema = `
CREATE TABLE IF NOT EXISTS node_accounting_identities(node_id TEXT NOT NULL REFERENCES nodes(id),accounting_id TEXT NOT NULL,subject_type TEXT NOT NULL,subject_id TEXT NOT NULL,PRIMARY KEY(node_id,accounting_id));
CREATE TABLE IF NOT EXISTS settled_leases(id TEXT PRIMARY KEY,node_id TEXT NOT NULL,user_id TEXT NOT NULL,accounting_id TEXT NOT NULL,upload INTEGER NOT NULL,download INTEGER NOT NULL,budget INTEGER NOT NULL,rate_version INTEGER NOT NULL);
`

func migratePortal(tx *sql.Tx) error {
	_, err := tx.Exec(accountingSchema + `
DROP INDEX IF EXISTS one_pending_subscription_request;
CREATE TABLE IF NOT EXISTS portal_templates(id TEXT PRIMARY KEY,config TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS portal_users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE COLLATE NOCASE,password TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,created_at INTEGER NOT NULL,revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS portal_subscriptions(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES portal_users(id),template_id TEXT NOT NULL REFERENCES portal_templates(id),created_at INTEGER NOT NULL,expires INTEGER NOT NULL,revoked INTEGER NOT NULL DEFAULT 0,revision INTEGER NOT NULL DEFAULT 1,grant_key TEXT NOT NULL,subscription_hash TEXT NOT NULL DEFAULT '',subscription_secret TEXT NOT NULL DEFAULT '',UNIQUE(user_id,grant_key));
CREATE INDEX IF NOT EXISTS subscriptions_user ON portal_subscriptions(user_id,created_at);
CREATE UNIQUE INDEX IF NOT EXISTS subscription_tokens ON portal_subscriptions(subscription_hash) WHERE subscription_hash!='';
CREATE TABLE IF NOT EXISTS portal_usage(subscription_id TEXT PRIMARY KEY REFERENCES portal_subscriptions(id),upload INTEGER NOT NULL DEFAULT 0,download INTEGER NOT NULL DEFAULT 0,reserved INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS portal_requests(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES portal_users(id),template_id TEXT NOT NULL REFERENCES portal_templates(id),status TEXT NOT NULL,requested_at INTEGER NOT NULL,decided_at INTEGER NOT NULL DEFAULT 0,note TEXT NOT NULL DEFAULT '',subscription_id TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1,request_key TEXT NOT NULL,UNIQUE(user_id,request_key));
CREATE TABLE IF NOT EXISTS portal_credentials(user_id TEXT NOT NULL REFERENCES portal_users(id),subscription_id TEXT NOT NULL REFERENCES portal_subscriptions(id),node_id TEXT NOT NULL REFERENCES nodes(id),scope TEXT NOT NULL,password_hash TEXT NOT NULL UNIQUE,password_secret TEXT NOT NULL,PRIMARY KEY(subscription_id,node_id,scope));
CREATE TABLE IF NOT EXISTS portal_sessions(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES portal_users(id),kind TEXT NOT NULL,client_name TEXT NOT NULL,installation_id TEXT NOT NULL DEFAULT '',access_hash TEXT NOT NULL UNIQUE,refresh_hash TEXT NOT NULL UNIQUE,csrf TEXT NOT NULL,access_expires INTEGER NOT NULL,expires INTEGER NOT NULL,created_at INTEGER NOT NULL,last_seen INTEGER NOT NULL,revoked INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT '{}',selected_subscription_id TEXT NOT NULL DEFAULT '',selection_revision INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS portal_spent_refresh(hash TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES portal_sessions(id),expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS portal_authorizations(id TEXT PRIMARY KEY,challenge TEXT NOT NULL,redirect_uri TEXT NOT NULL,state TEXT NOT NULL,installation_id TEXT NOT NULL,client_name TEXT NOT NULL,expires INTEGER NOT NULL,user_id TEXT NOT NULL DEFAULT '',code_hash TEXT NOT NULL DEFAULT '',consumed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS portal_leases(id TEXT PRIMARY KEY,node_id TEXT NOT NULL REFERENCES nodes(id),connection_id TEXT NOT NULL,user_id TEXT NOT NULL REFERENCES portal_users(id),subscription_id TEXT NOT NULL REFERENCES portal_subscriptions(id),session_id TEXT NOT NULL,revision INTEGER NOT NULL,created_at INTEGER NOT NULL,expires INTEGER NOT NULL,bandwidth INTEGER NOT NULL,budget INTEGER NOT NULL,upload INTEGER NOT NULL DEFAULT 0,download INTEGER NOT NULL DEFAULT 0,closed INTEGER NOT NULL DEFAULT 0,revoked INTEGER NOT NULL DEFAULT 0,UNIQUE(node_id,connection_id));
CREATE INDEX IF NOT EXISTS portal_lease_subscription ON portal_leases(subscription_id,expires,closed);
CREATE INDEX IF NOT EXISTS portal_lease_user ON portal_leases(user_id,session_id);
CREATE TABLE IF NOT EXISTS portal_rates(lease_id TEXT PRIMARY KEY REFERENCES portal_leases(id),version INTEGER NOT NULL DEFAULT 1,ack INTEGER NOT NULL DEFAULT 0,ceiling INTEGER NOT NULL DEFAULT 0,pending INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS portal_turnstile_tokens(hash TEXT PRIMARY KEY,expires INTEGER NOT NULL);
INSERT OR IGNORE INTO metadata(key,value) VALUES('portal_enabled','false'),('portal_registration','false');
UPDATE metadata SET value='5' WHERE key='schema_version';
`)
	return err
}

// A deployment must explicitly confirm that old development connections have
// settled. Merely opening an old database never deletes users without this gate.
func migrateSubscriptions(tx *sql.Tx) error {
	var users, plans, open int
	if err := tx.QueryRow("SELECT COUNT(*) FROM portal_users").Scan(&users); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM portal_plans").Scan(&plans); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE closed=0").Scan(&open); err != nil {
		return err
	}
	if users > 0 || plans > 0 || open > 0 {
		var ready string
		_ = tx.QueryRow("SELECT value FROM metadata WHERE key='subscriptions_reset_ready'").Scan(&ready)
		if ready != "true" {
			return errors.New("订阅升级需要先备份、结束旧连接并显式确认 subscriptions_reset_ready")
		}
		if open != 0 {
			return errors.New("旧连接尚未完成最终用量结算，拒绝清理开发账户")
		}
	}
	if _, err := tx.Exec(accountingSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT OR IGNORE INTO node_accounting_identities SELECT node_id,'user_'||user_id,'historical','' FROM portal_leases;
INSERT OR IGNORE INTO node_accounting_identities SELECT node_id,device_id,'historical','' FROM credentials;
INSERT OR IGNORE INTO node_accounting_identities SELECT node_id,device_id,'historical','' FROM daily;
INSERT OR IGNORE INTO node_accounting_identities SELECT node_id,device_id,'historical','' FROM minute;
INSERT OR IGNORE INTO node_accounting_identities SELECT node_id,device_id,'historical','' FROM hourly;
INSERT OR IGNORE INTO node_accounting_identities SELECT node_id,device_id,'historical','' FROM lifetime;
INSERT OR IGNORE INTO settled_leases SELECT l.id,l.node_id,l.user_id,'user_'||l.user_id,l.upload,l.download,l.budget,COALESCE(r.version,1) FROM portal_leases l LEFT JOIN portal_rates r ON r.lease_id=l.id WHERE l.closed=1;
DROP TABLE portal_rates;
DROP TABLE portal_leases;
DROP TABLE portal_credentials;
DROP TABLE portal_usage;
DROP TABLE portal_spent_refresh;
DROP TABLE portal_sessions;
DROP TABLE portal_authorizations;
DROP TABLE portal_users;
DROP TABLE portal_plans;
DELETE FROM portal_turnstile_tokens;
UPDATE metadata SET value=COALESCE((SELECT value FROM metadata WHERE key='subscriptions_registration_after_reset'),value) WHERE key='portal_registration';
DELETE FROM metadata WHERE key='subscriptions_registration_after_reset';
DELETE FROM metadata WHERE key IN ('subscriptions_reset_ready','subscriptions_migration_id','online_ips_version') OR key LIKE 'portal_recovery_%';
INSERT INTO metadata(key,value) VALUES('portal_recovery_hold','false');
`); err != nil {
		return err
	}
	if err := migratePortal(tx); err != nil {
		return err
	}
	return migrateOnlineIPs(tx)
}

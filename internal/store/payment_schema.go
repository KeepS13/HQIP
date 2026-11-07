package store

import (
	"database/sql"
	"strings"
	"time"
)

// PaymentSchemaSQL uses the common SQLite/PostgreSQL subset. PostgreSQL's
// explicit migrator executes each statement separately using its DDL role.
const PaymentSchemaSQL = `
CREATE TABLE IF NOT EXISTS payment_config_versions(version BIGINT PRIMARY KEY,api_base_url TEXT NOT NULL,merchant_num TEXT NOT NULL,pay_type TEXT NOT NULL,pay_duration_minutes BIGINT NOT NULL,query_enabled BIGINT NOT NULL,secret TEXT NOT NULL,identity TEXT NOT NULL,created_at BIGINT NOT NULL,revoked BIGINT NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS payment_config_identity ON payment_config_versions(identity,version);
CREATE TABLE IF NOT EXISTS payment_settings(id BIGINT PRIMARY KEY CHECK(id=1),enabled BIGINT NOT NULL DEFAULT 0,current_version BIGINT NOT NULL DEFAULT 0,revision BIGINT NOT NULL DEFAULT 0,updated_at BIGINT NOT NULL DEFAULT 0,last_query_at BIGINT NOT NULL DEFAULT 0);
INSERT INTO payment_settings(id) VALUES(1) ON CONFLICT(id) DO NOTHING;
CREATE TABLE IF NOT EXISTS payment_orders(id TEXT PRIMARY KEY,order_no TEXT NOT NULL UNIQUE,user_id TEXT NOT NULL REFERENCES portal_users(id),template_id TEXT NOT NULL REFERENCES portal_templates(id),request_key TEXT NOT NULL,template_revision BIGINT NOT NULL,template_name TEXT NOT NULL,template_snapshot TEXT NOT NULL,amount BIGINT NOT NULL CHECK(amount>=100 AND amount<=100000000),validity_days BIGINT NOT NULL,config_version BIGINT NOT NULL REFERENCES payment_config_versions(version),provider_identity TEXT NOT NULL,created_at BIGINT NOT NULL,expires_at BIGINT NOT NULL,paid_at BIGINT NOT NULL DEFAULT 0,fulfilled_at BIGINT NOT NULL DEFAULT 0,last_notify_at BIGINT NOT NULL DEFAULT 0,closed_at BIGINT NOT NULL DEFAULT 0,creation_state TEXT NOT NULL DEFAULT 'creating',payment_state TEXT NOT NULL DEFAULT 'unpaid',fulfillment_state TEXT NOT NULL DEFAULT 'none',subscription_id TEXT NOT NULL DEFAULT '',pay_url TEXT NOT NULL DEFAULT '',platform_order_no TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',last_reconcile_at BIGINT NOT NULL DEFAULT 0,revision BIGINT NOT NULL DEFAULT 1,UNIQUE(user_id,request_key));
CREATE UNIQUE INDEX IF NOT EXISTS payment_platform_order ON payment_orders(provider_identity,platform_order_no) WHERE platform_order_no!='';
CREATE INDEX IF NOT EXISTS payment_user_orders ON payment_orders(user_id,created_at,id);
CREATE INDEX IF NOT EXISTS payment_unfulfilled ON payment_orders(payment_state,fulfillment_state,created_at);
CREATE TABLE IF NOT EXISTS payment_activities(id TEXT PRIMARY KEY,order_id TEXT NOT NULL REFERENCES payment_orders(id),at BIGINT NOT NULL,kind TEXT NOT NULL,message TEXT NOT NULL,signature_version BIGINT NOT NULL DEFAULT 0,reported_platform_order_no TEXT NOT NULL DEFAULT '',actual_amount BIGINT NOT NULL DEFAULT 0,reported_paid_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS payment_activity_order ON payment_activities(order_id,at,id);
INSERT INTO metadata(key,value) VALUES('payments_schema_version','1') ON CONFLICT(key) DO NOTHING;
`

func EnsurePaymentSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var initialized int
	if err = tx.QueryRow("SELECT COUNT(*) FROM metadata WHERE key='payments_schema_version'").Scan(&initialized); err != nil {
		return err
	}
	for _, statement := range strings.Split(PaymentSchemaSQL, ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	if initialized == 0 {
		if _, err = tx.Exec("UPDATE portal_requests SET status='cancelled',note='支付购买流程已启用，原申请流程已停止',decided_at=?,revision=revision+1 WHERE status='pending'", time.Now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

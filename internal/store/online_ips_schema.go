package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Both SQL migrations and the SQLite-to-PostgreSQL importer use this DDL.
// client_ip is runtime business identity; historical unknown peers stay empty.
const onlineIPSchema = `
ALTER TABLE portal_leases ADD COLUMN client_ip TEXT NOT NULL DEFAULT '';
CREATE INDEX portal_lease_ip ON portal_leases(subscription_id,client_ip,closed,expires);
`

// migrateOnlineIPs extends the existing connection ledger. Historical rows
// deliberately retain an empty address: neither a download IP nor a guessed
// peer may turn several unknown reservations into one. The normal offline
// migration command owns DDL, including PostgreSQL's migration-role boundary.
func migrateOnlineIPs(tx *sql.Tx) error {
	var version string
	err := tx.QueryRow("SELECT value FROM metadata WHERE key='online_ips_version'").Scan(&version)
	if err == nil && version != "1" {
		return errors.New("online IP schema is incompatible")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		for _, statement := range strings.Split(onlineIPSchema, ";") {
			if strings.TrimSpace(statement) != "" {
				if _, err = tx.Exec(statement); err != nil {
					return err
				}
			}
		}
		if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('online_ips_version','1')"); err != nil {
			return err
		}
	}
	// Version 3 changes counting, not the schema. Persist the contract once at
	// migration time so rollback cannot silently restore IP deduplication for HY2.
	err = tx.QueryRow("SELECT value FROM metadata WHERE key='connection_slots_version'").Scan(&version)
	if err == nil && version != "1" && version != "2" && version != "3" {
		return errors.New("online slots contract is incompatible")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('connection_slots_version','3') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	return err
}

package store

import (
	"context"
	"database/sql"
	"github.com/KeepS13/HQIP/internal/secure"
)

type integrityQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// All persisted reversible secrets must authenticate with the same master key.
// Password hashes are intentionally not part of the encryption vault.
func validateVault(ctx context.Context, q integrityQuery, vault *secure.Vault) error {
	rows, err := q.QueryContext(ctx, `SELECT password FROM credentials
UNION ALL SELECT agent_secret FROM nodes UNION ALL SELECT password FROM nodes
UNION ALL SELECT token_secret FROM devices
UNION ALL SELECT subscription_secret FROM portal_subscriptions
UNION ALL SELECT password_secret FROM portal_credentials
UNION ALL SELECT token FROM cloudflare_settings
UNION ALL SELECT private_key FROM certificate_accounts
UNION ALL SELECT bundle FROM certificate_versions
UNION ALL SELECT value FROM metadata WHERE key='turnstile_secret'
UNION ALL SELECT COALESCE(json_extract(value,'$.secret'),'') FROM metadata WHERE key='turnstile_settings'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var encrypted string
		if err = rows.Scan(&encrypted); err != nil {
			return err
		}
		if _, err = vault.Decrypt(encrypted); err != nil {
			return secretError(err)
		}
	}
	return rows.Err()
}

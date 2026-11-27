package store

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"

	"github.com/KeepS13/HQIP/internal/secure"
)

// Account import credentials are encrypted business settings in metadata, like
// node Reality secrets. Keyed by owner, they require no scan or schema migration.
// The public owner ID is routing information; only the random 256-bit secret
// grants access. Per-subscription accounting and proxy credentials stay separate.
const accountImportKey = "portal-account-import:"

func (s *Store) portalAccountImportLink(user, action string) (string, error) {
	if action != "get" && action != "create" && action != "rotate" && action != "revoke" {
		return "", errors.New("导入链接操作无效")
	}
	return retryDatabase(func() (string, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return "", err
		}
		defer tx.Rollback()
		// Lock the user before changing child credentials, matching purchases and
		// single-link writes. Failed rotation rolls back both URL and revocation.
		if action != "get" {
			if err = lockAllocationUser(tx, user); err != nil {
				return "", err
			}
		}
		var enabled bool
		if err = tx.QueryRow("SELECT enabled FROM portal_users WHERE id=?", user).Scan(&enabled); err != nil {
			return "", notFound(err)
		}
		if !enabled {
			return "", ErrEntitlement
		}
		var enc string
		err = tx.QueryRow("SELECT value FROM metadata WHERE key=?", accountImportKey+user).Scan(&enc)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if action == "rotate" || action == "revoke" {
			if err = revokeImportCredentials(tx, user, ""); err != nil {
				return "", err
			}
			if _, err = tx.Exec("DELETE FROM metadata WHERE key=?", accountImportKey+user); err != nil {
				return "", err
			}
			enc = ""
		}
		if action == "revoke" || action == "get" && enc == "" {
			return "", tx.Commit()
		}
		var token string
		if enc != "" {
			token, err = s.Vault.Decrypt(enc)
		} else {
			token = secure.Token()
			enc, err = s.Vault.Encrypt(token)
			if err == nil {
				_, err = tx.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", accountImportKey+user, enc)
			}
		}
		if err != nil {
			return "", err
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return s.BaseURL + "/usub/all." + user + "." + token, nil
	})
}

// Resolve the credential and current membership in one snapshot. Empty accounts
// are valid and export a deny-by-default empty config, never proxy permission.
// Single-link rotation does not change this independent account capability.
func (s *Store) resolveAccountImport(token string) (string, []string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" || len(parts[1]) > 128 || len(parts[2]) != 43 {
		return "", nil, ErrUnauthorized
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	var enc string
	err = tx.QueryRow("SELECT m.value FROM metadata m JOIN portal_users u ON m.key=? WHERE u.id=? AND u.enabled=1", accountImportKey+parts[1], parts[1]).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrUnauthorized
	}
	if err != nil {
		return "", nil, err
	}
	secret, err := s.Vault.Decrypt(enc)
	if err != nil {
		return "", nil, err
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(parts[2])) != 1 {
		return "", nil, ErrUnauthorized
	}
	ids, err := s.importSubscriptionIDs(tx, parts[1], "", "get")
	if err != nil {
		return "", nil, err
	}
	return parts[1], ids, tx.Commit()
}

package store

import (
	"context"

	"github.com/KeepS13/HQIP/internal/secure"
)

// AccountImportPreparation contains counts only. Capabilities must never be
// returned to administrators or emitted in deployment receipts.
type AccountImportPreparation struct {
	Users    int `json:"users"`
	Created  int `json:"created"`
	Existing int `json:"existing"`
}

// PreparePortalAccountImports explicitly prepares missing account links for all
// current users, including empty/disabled accounts. Disabled users remain denied
// by the normal resolver. It never changes single links, passwords or leases.
// One transaction locks missing owners in ID order and commits all or nothing;
// concurrent preparation/rotation uses the same user lock and serializable retry.
// Repeating this operation preserves existing capabilities. This is an explicit
// operator action, never a startup job that silently recreates revoked links.
func (s *Store) PreparePortalAccountImports(ctx context.Context) (AccountImportPreparation, error) {
	return retryDatabase(func() (AccountImportPreparation, error) {
		out := AccountImportPreparation{}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `SELECT u.id,CASE WHEN m.key IS NULL THEN 0 ELSE 1 END
 FROM portal_users u LEFT JOIN metadata m ON m.key=? || u.id ORDER BY u.id`, accountImportKey)
		if err != nil {
			return out, err
		}
		missing := []string{}
		for rows.Next() {
			var user string
			var exists bool
			if err = rows.Scan(&user, &exists); err != nil {
				rows.Close()
				return out, err
			}
			out.Users++
			if !exists {
				missing = append(missing, user)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		for _, user := range missing {
			if err = lockAllocationUser(tx, user); err != nil {
				return out, err
			}
			enc, err := s.Vault.Encrypt(secure.Token())
			if err != nil {
				return out, err
			}
			result, err := tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", accountImportKey+user, enc)
			if err != nil {
				return out, err
			}
			created, err := result.RowsAffected()
			if err != nil {
				return out, err
			}
			out.Created += int(created)
		}
		out.Existing = out.Users - out.Created
		return out, tx.Commit()
	})
}

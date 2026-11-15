package store

import (
	"strings"

	"github.com/KeepS13/HQIP/internal/secure"
	"golang.org/x/crypto/bcrypt"
)

// Only the one-time importer writes this marker. Raw bcrypt hashes are not
// accepted, and the shared administrator/device password verifier is unchanged.
const legacyPortalPasswordPrefix = "legacy-bcrypt:"

func checkPortalPassword(encoded, password string) (valid, upgrade bool) {
	if !strings.HasPrefix(encoded, legacyPortalPasswordPrefix) {
		return secure.CheckPassword(encoded, password), false
	}
	hash := strings.TrimPrefix(encoded, legacyPortalPasswordPrefix)
	if len(hash) != 60 || len(password) > 1024 {
		return false, false
	}
	// The source service uses cost 10. Bound work even for malformed imports.
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost != 10 {
		return false, false
	}
	if err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return false, false
	}
	return true, true
}

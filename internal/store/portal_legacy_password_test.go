package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/secure"
	"golang.org/x/crypto/bcrypt"
)

func legacyPasswordForTest(t *testing.T, password string) string {
	t.Helper()
	// Node bcrypt accepts longer UTF-8 input and uses its first 72 bytes.
	input := []byte(password)
	if len(input) > 72 {
		input = input[:72]
	}
	hash, err := bcrypt.GenerateFromPassword(input, 10)
	if err != nil {
		t.Fatal(err)
	}
	return legacyPortalPasswordPrefix + string(hash)
}

func importedPasswordUser(t *testing.T, s *Store, email, hash string) string {
	t.Helper()
	id := NewID()
	if _, err := s.DB.Exec("INSERT INTO portal_users(id,email,password,created_at) VALUES(?,?,?,?)", id, email, hash, s.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPortalLegacyPasswordLoginUpgrade(t *testing.T) {
	for _, password := range []string{"original-password-123", "原密码中文123", strings.Repeat("x", 72), strings.Repeat("密", 25)} {
		t.Run(password[:6], func(t *testing.T) {
			s, _ := fixture(t)
			legacy := legacyPasswordForTest(t, password)
			id := importedPasswordUser(t, s, "legacy@example.com", legacy)
			if _, _, err := s.PortalLogin("legacy@example.com", "wrong-password"); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("wrong password accepted", err)
			}
			tokens, _, err := s.PortalLogin(" LEGACY@example.com ", password)
			if err != nil || tokens.User.ID != id {
				t.Fatal("original password rejected", err)
			}
			var hash string
			var revision int64
			if err = s.DB.QueryRow("SELECT password,revision FROM portal_users WHERE id=?", id).Scan(&hash, &revision); err != nil {
				t.Fatal(err)
			}
			if !secure.CheckPassword(hash, password) || revision != 2 {
				t.Fatal("password was not upgraded")
			}
			if _, _, err = s.PortalLogin("legacy@example.com", password); err != nil {
				t.Fatal("second login", err)
			}
			var after string
			_ = s.DB.QueryRow("SELECT password FROM portal_users WHERE id=?", id).Scan(&after)
			if after != hash {
				t.Fatal("Argon2 password rewritten")
			}
		})
	}
}

func TestPortalLegacyPasswordChangeAndIsolation(t *testing.T) {
	s, _ := fixture(t)
	legacy := legacyPasswordForTest(t, "original-password")
	id := importedPasswordUser(t, s, "legacy@example.com", legacy)
	if secure.CheckPassword(legacy, "original-password") {
		t.Fatal("legacy format accepted by administrator/device verifier")
	}
	if ok, _ := checkPortalPassword(strings.TrimPrefix(legacy, legacyPortalPasswordPrefix), "original-password"); ok {
		t.Fatal("unmarked bcrypt accepted")
	}
	if err := s.PortalPassword(id, "wrong", "new-password", false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if err := s.PortalPassword(id, "original-password", "new-password", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PortalLogin("legacy@example.com", "original-password"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old password still accepted", err)
	}
	if _, _, err := s.PortalLogin("legacy@example.com", "new-password"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{legacyPortalPasswordPrefix, legacyPortalPasswordPrefix + "$2b$31$" + strings.Repeat("a", 53), legacyPortalPasswordPrefix + strings.Repeat("x", 60)} {
		if ok, _ := checkPortalPassword(value, "original-password"); ok {
			t.Fatal("invalid legacy hash accepted")
		}
	}
}

func TestPortalLegacyPasswordDisabledAndSessionFailure(t *testing.T) {
	s, _ := fixture(t)
	legacy := legacyPasswordForTest(t, "original-password")
	id := importedPasswordUser(t, s, "legacy@example.com", legacy)
	_, _ = s.DB.Exec("UPDATE portal_users SET enabled=0 WHERE id=?", id)
	if _, _, err := s.PortalLogin("legacy@example.com", "original-password"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled account accepted", err)
	}
	_, _ = s.DB.Exec("UPDATE portal_users SET enabled=1 WHERE id=?", id)
	for i := 0; i < 64; i++ {
		_, err := s.DB.Exec(`INSERT INTO portal_sessions(id,user_id,kind,client_name,access_hash,refresh_hash,csrf,access_expires,expires,created_at,last_seen) VALUES(?,?,'browser','test',?,?,?,?,?,?,?)`, NewID(), id, secure.Token(), secure.Token(), secure.Token(), s.Now().Unix()+3600, s.Now().Unix()+3600, s.Now().Unix(), s.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.PortalLogin("legacy@example.com", "original-password"); err == nil {
		t.Fatal("session limit bypassed")
	}
	var hash string
	var revision int64
	_ = s.DB.QueryRow("SELECT password,revision FROM portal_users WHERE id=?", id).Scan(&hash, &revision)
	if hash != legacy || revision != 1 {
		t.Fatal("failed login committed the password upgrade")
	}
}

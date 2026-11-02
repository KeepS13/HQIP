package secure

import (
	"encoding/base64"
	"path/filepath"
	"testing"
)

func TestVaultAuthenticatesCiphertextAndKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	v, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := v.Encrypt("server-password")
	if err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := again.Decrypt(encrypted)
	if err != nil || plain != "server-password" {
		t.Fatal("key did not survive reopening")
	}
	bytes, _ := base64.RawStdEncoding.DecodeString(encrypted)
	bytes[len(bytes)-1] ^= 1
	if _, err = v.Decrypt(base64.RawStdEncoding.EncodeToString(bytes)); err == nil {
		t.Fatal("modified ciphertext was accepted")
	}
	wrong, err := Open(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Decrypt(encrypted); err == nil {
		t.Fatal("wrong master key was accepted")
	}
}
func TestPasswordHash(t *testing.T) {
	hash := HashPassword("correct-long-password")
	if !CheckPassword(hash, "correct-long-password") || CheckPassword(hash, "wrong-long-password") || CheckPassword("invalid", "correct-long-password") {
		t.Fatal("password authentication is incorrect")
	}
}

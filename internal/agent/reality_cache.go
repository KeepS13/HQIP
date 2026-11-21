package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

// Reality must recover its HY2 backend credential locally during a website
// outage. Cache only ciphertext bound to the node's existing secret and UUID;
// the independent persisted quota grant still decides whether it may be used.
func (a *Agent) realityCacheCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("hqip-reality-cache-v1:" + a.cfg.NodeID + ":" + a.cfg.Token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (a *Agent) rememberRealityPassword(uuid, password string) error {
	box, err := a.realityCacheCipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, box.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	sealed := box.Seal(nonce, nonce, []byte(password), []byte(uuid))
	a.ledger.mu.Lock()
	defer a.ledger.mu.Unlock()
	if _, err = a.ledger.db.Exec("CREATE TABLE IF NOT EXISTS reality_credentials(uuid TEXT PRIMARY KEY,secret TEXT NOT NULL)"); err != nil {
		return err
	}
	_, err = a.ledger.db.Exec("INSERT INTO reality_credentials(uuid,secret) VALUES(?,?) ON CONFLICT(uuid) DO UPDATE SET secret=excluded.secret", uuid, base64.RawStdEncoding.EncodeToString(sealed))
	return err
}

func (a *Agent) forgetRealityPassword(uuid string) {
	a.ledger.mu.Lock()
	defer a.ledger.mu.Unlock()
	_, _ = a.ledger.db.Exec("DELETE FROM reality_credentials WHERE uuid=?", uuid)
}

func (a *Agent) cachedRealityPassword(uuid string) (string, error) {
	denied := errors.New("Reality offline credential unavailable")
	a.ledger.mu.Lock()
	var sealed string
	err := a.ledger.db.QueryRow("SELECT secret FROM reality_credentials WHERE uuid=?", uuid).Scan(&sealed)
	a.ledger.mu.Unlock()
	if err != nil {
		return "", denied
	}
	box, err := a.realityCacheCipher()
	if err != nil {
		return "", denied
	}
	raw, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < box.NonceSize() {
		return "", denied
	}
	plain, err := box.Open(nil, raw[:box.NonceSize()], raw[box.NonceSize():], []byte(uuid))
	if err != nil || len(plain) != 43 || model.RealityUUID(string(plain)) != uuid {
		return "", denied
	}
	states, err := a.ledger.offlineStates()
	if err != nil {
		return "", denied
	}
	hash := secure.Hash(string(plain))
	for _, s := range states {
		g := s.Grant
		if g.NodeQuota != nil && g.Billing != nil && g.Billing.Protocol == "vless" && g.CredentialHash == hash && !s.Denied && validOfflineGrant(g, a.cfg.NodeID, hash, a.now()) {
			return string(plain), nil
		}
	}
	return "", denied
}

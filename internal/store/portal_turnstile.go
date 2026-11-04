package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/KeepS13/HQIP/internal/secure"
	"regexp"
	"strings"
	"time"
)

type TurnstileSettings struct {
	SiteKey          string   `json:"siteKey"`
	AllowedHostnames []string `json:"allowedHostnames"`
	SecretConfigured bool     `json:"secretConfigured"`
	Revision         int64    `json:"revision"`
}
type storedTurnstile struct {
	TurnstileSettings
	Secret string `json:"secret"`
}

func (s *Store) turnstileStored() (storedTurnstile, error) {
	var v storedTurnstile
	v.AllowedHostnames = []string{}
	raw := s.PortalSetting("turnstile_settings")
	if raw != "" {
		err := json.Unmarshal([]byte(raw), &v)
		return v, err
	}
	v.SiteKey = s.PortalSetting("turnstile_site_key")
	v.Secret = s.PortalSetting("turnstile_secret")
	v.SecretConfigured = v.Secret != ""
	return v, nil
}
func (s *Store) TurnstileSettings() (TurnstileSettings, error) {
	v, e := s.turnstileStored()
	return v.TurnstileSettings, e
}
func (s *Store) TurnstileSecret() (string, error) {
	v, e := s.turnstileStored()
	if e != nil {
		return "", e
	}
	return s.Vault.Decrypt(v.Secret)
}
func (s *Store) TurnstileVerificationConfig() (TurnstileSettings, string, error) {
	v, err := s.turnstileStored()
	if err != nil {
		return v.TurnstileSettings, "", err
	}
	secret, err := s.Vault.Decrypt(v.Secret)
	return v.TurnstileSettings, secret, err
}

var turnstileHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

func (s *Store) SaveTurnstile(site string, hosts []string, secret *string, expected int64) (TurnstileSettings, error) {
	var empty TurnstileSettings
	if site == "" || len(site) > 200 || strings.ContainsAny(site, "\r\n\x00") {
		return empty, errors.New("无效Site Key")
	}
	if len(hosts) == 0 || len(hosts) > 10 {
		return empty, errors.New("请配置1至10个允许域名")
	}
	seen := map[string]bool{}
	for i, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if !turnstileHostname.MatchString(h) || strings.Contains(h, "..") || seen[h] {
			return empty, errors.New("允许域名无效")
		}
		seen[h] = true
		hosts[i] = h
	}
	if secret != nil && (len(*secret) < 16 || len(*secret) > 500) {
		return empty, errors.New("Secret Key无效")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRow("SELECT value FROM metadata WHERE key='turnstile_settings'").Scan(&raw)
	var old storedTurnstile
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &old); err != nil {
			return empty, err
		}
	} else if err == sql.ErrNoRows {
		_ = tx.QueryRow("SELECT value FROM metadata WHERE key='turnstile_secret'").Scan(&old.Secret)
	} else {
		return empty, err
	}
	if old.Revision != expected {
		return empty, ErrConflict
	}
	if secret != nil {
		old.Secret, err = s.Vault.Encrypt(*secret)
		if err != nil {
			return empty, err
		}
	}
	if old.Secret == "" {
		return empty, errors.New("请配置Secret Key")
	}
	old.TurnstileSettings = TurnstileSettings{SiteKey: site, AllowedHostnames: hosts, SecretConfigured: true, Revision: old.Revision + 1}
	data, _ := json.Marshal(old)
	if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('turnstile_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(data)); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("DELETE FROM metadata WHERE key IN ('turnstile_site_key','turnstile_secret')"); err != nil {
		return empty, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'turnstile.update','settings')", s.Now().Unix()); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return old.TurnstileSettings, nil
}
func (s *Store) ConsumeTurnstile(token string) bool {
	tx, err := s.DB.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM portal_turnstile_tokens WHERE expires<=?", s.Now().Unix()); err != nil {
		return false
	}
	var n int
	if tx.QueryRow("SELECT COUNT(*) FROM portal_turnstile_tokens").Scan(&n) != nil || n >= 10000 {
		return false
	}
	if _, err = tx.Exec("INSERT INTO portal_turnstile_tokens(hash,expires) VALUES(?,?)", secure.Hash(token), s.Now().Add(10*time.Minute).Unix()); err != nil {
		return false
	}
	return tx.Commit() == nil
}

package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/KeepS13/HQIP/internal/rules"
)

type RulesVersion struct {
	Version int64        `json:"version"`
	Hash    string       `json:"hash"`
	Bundle  rules.Bundle `json:"bundle"`
}

func (s *Store) Rules() (RulesVersion, error) {
	var result RulesVersion
	var body []byte
	err := s.DB.QueryRow("SELECT version,hash,body FROM rule_versions ORDER BY version DESC LIMIT 1").Scan(&result.Version, &result.Hash, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return s.SaveRules(rules.Default())
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(body, &result.Bundle)
	return result, err
}
func (s *Store) SaveRules(bundle rules.Bundle) (RulesVersion, error) {
	var result RulesVersion
	body, err := rules.Canonical(bundle)
	if err != nil {
		return result, err
	}
	hash, err := rules.Hash(bundle)
	if err != nil {
		return result, err
	}
	if len(body) > 8<<20 {
		return result, errors.New("规则文件不能超过 8 MiB")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var latestHash string
	var latest int64
	err = tx.QueryRow("SELECT hash,version FROM rule_versions ORDER BY version DESC LIMIT 1").Scan(&latestHash, &latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if latestHash == hash {
		tx.Rollback()
		return s.Rules()
	}
	// A repeated historical hash is content-identical; store a fresh monotonically
	// increasing rule version while retaining the immutable bytes by hash.
	if _, err = tx.Exec("DELETE FROM rule_versions WHERE hash=?", hash); err != nil {
		return result, err
	}
	if _, err = tx.Exec("INSERT INTO rule_versions(hash,version,body,created_at) VALUES(?,?,?,?)", hash, latest+1, body, s.Now().Unix()); err != nil {
		return result, err
	}
	if _, err = tx.Exec("UPDATE device_config SET revision=revision+1"); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	result.Version = latest + 1
	result.Hash = hash
	err = json.Unmarshal(body, &result.Bundle)
	s.Audit("rules.update", hash)
	return result, err
}
func (s *Store) RuleBytes(hash string) ([]byte, error) {
	var b []byte
	err := s.DB.QueryRow("SELECT body FROM rule_versions WHERE hash=?", hash).Scan(&b)
	return b, notFound(err)
}

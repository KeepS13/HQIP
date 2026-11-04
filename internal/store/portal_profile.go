package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func (s *Store) PortalSubscription(user, action string) (string, error) {
	var token string
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var enc string
	if err = tx.QueryRow("SELECT subscription_secret FROM portal_users WHERE id=?", user).Scan(&enc); err != nil {
		return "", notFound(err)
	}
	if action == "get" || action == "create" && enc != "" {
		if enc != "" {
			token, err = s.Vault.Decrypt(enc)
			if err != nil {
				return "", err
			}
		}
	} else {
		if action != "revoke" {
			token = secure.Token()
			enc, err = s.Vault.Encrypt(token)
			if err != nil {
				return "", err
			}
		} else {
			enc = ""
		}
		hash := ""
		if token != "" {
			hash = secure.Hash(token)
		}
		if _, err = tx.Exec("UPDATE portal_users SET subscription_hash=?,subscription_secret=?,revision=revision+1 WHERE id=?", hash, enc, user); err != nil {
			return "", err
		}
		if _, err = tx.Exec("DELETE FROM portal_credentials WHERE user_id=? AND scope='subscription'", user); err != nil {
			return "", err
		}
		if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE user_id=? AND session_id='subscription'", user); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	if token == "" {
		return "", nil
	}
	return s.BaseURL + "/usub/" + token, nil
}
func (s *Store) ResolvePortalSubscription(token string) (string, error) {
	if len(token) != 43 {
		return "", ErrUnauthorized
	}
	var id string
	err := s.DB.QueryRow("SELECT id FROM portal_users WHERE subscription_hash=? AND enabled=1", secure.Hash(token)).Scan(&id)
	if err != nil {
		return "", ErrUnauthorized
	}
	return id, nil
}
func (s *Store) PortalCredential(user, node, scope string) (string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var enc string
	err = tx.QueryRow("SELECT password_secret FROM portal_credentials WHERE user_id=? AND node_id=? AND scope=?", user, node, scope).Scan(&enc)
	if err == nil {
		return s.Vault.Decrypt(enc)
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	password := secure.Token()
	enc, err = s.Vault.Encrypt(password)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec("INSERT INTO portal_credentials(user_id,node_id,scope,password_hash,password_secret) VALUES(?,?,?,?,?)", user, node, scope, secure.Hash(password), enc); err != nil {
		return "", err
	}
	err = tx.Commit()
	return password, err
}
func (s *Store) PortalClientProfile(user, scope string) (model.ClientProfile, *model.Entitlement, error) {
	var p model.ClientProfile
	u, err := s.PortalUser(user)
	if err != nil {
		return p, nil, err
	}
	e := u.Entitlement
	p = model.ClientProfile{DeviceID: user, Name: u.Email, Enabled: u.Enabled, Nodes: []model.ClientNode{}}
	var revision int64
	if err = s.DB.QueryRow("SELECT revision FROM portal_users WHERE id=?", user).Scan(&revision); err != nil {
		return p, e, err
	}
	p.Revision = revision
	rules, err := s.Rules()
	if err != nil {
		return p, e, err
	}
	p.Rules = model.RuleReference{Version: rules.Version, Hash: rules.Hash, URL: s.BaseURL + "/api/v2/client/rules/" + rules.Hash}
	p.Revision += rules.Version
	if e == nil || e.Status != "active" {
		return p, e, nil
	}
	p.DefaultNodeID = e.DefaultNodeID
	for _, id := range e.NodeIDs {
		n, err := s.Node(id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return p, e, err
		}
		if n.Lifecycle != "active" {
			continue
		}
		pass, err := s.PortalCredential(user, id, scope)
		if err != nil {
			return p, e, err
		}
		ready := n.Status == "ready" && s.nodeAdmissionCapable(id)
		p.Nodes = append(p.Nodes, model.ClientNode{ID: n.ID, Name: n.Name, Server: n.PublicIP, PublicIP: n.PublicIP, Port: n.Port, Password: pass, SNI: n.Domain, Ready: ready})
	}
	var finalRevision int64
	if err = s.DB.QueryRow("SELECT revision FROM portal_users WHERE id=?", user).Scan(&finalRevision); err != nil {
		return p, e, err
	}
	if finalRevision != revision {
		return p, e, ErrConflict
	}
	return p, e, nil
}
func (s *Store) nodeAdmissionCapable(id string) bool {
	var raw string
	err := s.DB.QueryRow("SELECT capabilities FROM nodes WHERE id=?", id).Scan(&raw)
	return err == nil && containsCapability(raw, "admission-v1")
}
func containsCapability(raw, want string) bool {
	var values []string
	if json.Unmarshal([]byte(raw), &values) != nil {
		return false
	}
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

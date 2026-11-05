package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func (s *Store) PortalSubscription(user, subscription, action string) (string, error) {
	if action != "get" && action != "create" && action != "rotate" && action != "revoke" {
		return "", errors.New("导入链接操作无效")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	e, err := userSubscription(tx, user, subscription, s.Now())
	if err != nil {
		return "", err
	}
	if e.Status != "active" && action != "revoke" {
		if action == "get" {
			return "", nil
		}
		return "", ErrEntitlement
	}
	var enc string
	if err = tx.QueryRow("SELECT subscription_secret FROM portal_subscriptions WHERE id=? AND user_id=?", subscription, user).Scan(&enc); err != nil {
		return "", notFound(err)
	}
	var token string
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
		if _, err = tx.Exec("UPDATE portal_subscriptions SET subscription_hash=?,subscription_secret=?,revision=revision+1 WHERE id=?", hash, enc, subscription); err != nil {
			return "", err
		}
		if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user); err != nil {
			return "", err
		}
		if _, err = tx.Exec("DELETE FROM portal_credentials WHERE subscription_id=? AND scope='subscription'", subscription); err != nil {
			return "", err
		}
		if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE subscription_id=? AND session_id='subscription'", subscription); err != nil {
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
func (s *Store) ResolvePortalSubscription(token string) (string, string, error) {
	if len(token) != 43 {
		return "", "", ErrUnauthorized
	}
	var user, id string
	err := s.DB.QueryRow("SELECT s.user_id,s.id FROM portal_subscriptions s JOIN portal_users u ON u.id=s.user_id WHERE s.subscription_hash=? AND s.revoked=0 AND u.enabled=1", secure.Hash(token)).Scan(&user, &id)
	if err != nil {
		return "", "", ErrUnauthorized
	}
	return user, id, nil
}
func selectedSubscription(q portalQuery, user, session string, now time.Time) (string, int64, error) {
	var id string
	var revision int64
	err := q.QueryRow("SELECT selected_subscription_id,selection_revision FROM portal_sessions WHERE id=? AND user_id=? AND kind='native' AND revoked=0 AND expires>?", session, user, now.Unix()).Scan(&id, &revision)
	return id, revision, notFound(err)
}
func (s *Store) SelectedSubscription(user, session string) (string, int64, error) {
	return selectedSubscription(s.DB, user, session, s.Now())
}
func (s *Store) SelectSubscription(user, session, subscription string, expected int64) (model.SubscriptionSelection, error) {
	var result model.SubscriptionSelection
	tx, err := s.DB.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	current, revision, err := selectedSubscription(tx, user, session, s.Now())
	if err != nil {
		return result, err
	}
	result = model.SubscriptionSelection{SubscriptionID: current, SelectionRevision: revision}
	if current == subscription {
		return result, nil
	}
	if revision != expected {
		return result, ErrConflict
	}
	var occupied int
	if err = tx.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE user_id=? AND session_id=? AND closed=0 AND expires>?", user, session, s.Now().Unix()).Scan(&occupied); err != nil {
		return result, err
	}
	if occupied > 0 {
		return result, ErrConflict
	}
	if subscription != "" {
		e, err := userSubscription(tx, user, subscription, s.Now())
		if err != nil {
			return result, err
		}
		if e.Status != "active" {
			return result, ErrEntitlement
		}
	}
	if _, err = tx.Exec("UPDATE portal_sessions SET selected_subscription_id=?,selection_revision=selection_revision+1 WHERE id=?", subscription, session); err != nil {
		return result, err
	}
	if _, err = tx.Exec("DELETE FROM portal_credentials WHERE user_id=? AND scope=?", user, session); err != nil {
		return result, err
	}
	if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE user_id=? AND session_id=? AND closed=0", user, session); err != nil {
		return result, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.selected',?)", s.Now().Unix(), session+":"+subscription); err != nil {
		return result, err
	}
	result = model.SubscriptionSelection{SubscriptionID: subscription, SelectionRevision: revision + 1}
	return result, tx.Commit()
}
func (s *Store) DrainSubscriptionSession(user, session, subscription string, expected int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	selected, revision, err := selectedSubscription(tx, user, session, s.Now())
	if err != nil {
		return err
	}
	if selected != subscription || revision != expected {
		return ErrConflict
	}
	if _, err = tx.Exec("UPDATE portal_leases SET revoked=1 WHERE user_id=? AND session_id=? AND subscription_id=? AND closed=0", user, session, subscription); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) PortalCredential(user, subscription, node, scope string) (string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	e, err := userSubscription(tx, user, subscription, s.Now())
	if err != nil {
		return "", err
	}
	if !entitlementAllows(e, node) || !activeCredentialSession(tx, user, subscription, scope, s.Now()) {
		return "", ErrEntitlement
	}
	var enc string
	err = tx.QueryRow("SELECT password_secret FROM portal_credentials WHERE subscription_id=? AND node_id=? AND scope=?", subscription, node, scope).Scan(&enc)
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
	if _, err = tx.Exec("INSERT INTO portal_credentials(user_id,subscription_id,node_id,scope,password_hash,password_secret) VALUES(?,?,?,?,?,?)", user, subscription, node, scope, secure.Hash(password), enc); err != nil {
		return "", err
	}
	return password, tx.Commit()
}
func (s *Store) PortalClientProfile(user, scope, subscriptionID string) (model.ClientProfile, *model.UserSubscription, error) {
	p := model.ClientProfile{DeviceID: user, SubscriptionID: subscriptionID, Nodes: []model.ClientNode{}}
	var name string
	var revision int64
	if err := s.DB.QueryRow("SELECT email,enabled,revision FROM portal_users WHERE id=?", user).Scan(&name, &p.Enabled, &revision); err != nil {
		return p, nil, notFound(err)
	}
	p.Name = name
	p.Revision = revision
	if scope != "subscription" {
		selected, version, err := s.SelectedSubscription(user, scope)
		if err != nil {
			return p, nil, err
		}
		if selected != subscriptionID {
			return p, nil, ErrConflict
		}
		p.SelectionRevision = version
		p.Revision += version
	}
	rules, err := s.Rules()
	if err != nil {
		return p, nil, err
	}
	p.Rules = model.RuleReference{Version: rules.Version, Hash: rules.Hash, URL: s.BaseURL + "/api/v3/client/rules/" + rules.Hash}
	p.Revision += rules.Version
	if subscriptionID == "" {
		return p, nil, nil
	}
	e, err := s.UserSubscription(user, subscriptionID)
	if err != nil {
		return p, nil, err
	}
	p.Revision += e.Revision
	if e.Status != "active" {
		return p, e, nil
	}
	p.DefaultNodeID = e.DefaultNodeID
	for _, id := range e.NodeIDs {
		n, err := s.Node(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return p, e, err
		}
		if n.Lifecycle != "active" {
			continue
		}
		pass, err := s.PortalCredential(user, subscriptionID, id, scope)
		if err != nil {
			return p, e, err
		}
		p.Nodes = append(p.Nodes, model.ClientNode{ID: n.ID, Name: n.Name, Server: n.PublicIP, PublicIP: n.PublicIP, Port: n.Port, Password: pass, SNI: n.Domain, Ready: n.Status == "ready" && s.nodeAdmissionCapable(id)})
	}
	var finalRevision int64
	if err = s.DB.QueryRow("SELECT revision FROM portal_users WHERE id=?", user).Scan(&finalRevision); err != nil {
		return p, e, err
	}
	if finalRevision != revision {
		return p, e, ErrConflict
	}
	if scope != "subscription" {
		selected, version, err := s.SelectedSubscription(user, scope)
		if err != nil {
			return p, e, err
		}
		if selected != subscriptionID || version != p.SelectionRevision {
			return p, e, ErrConflict
		}
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

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

var accountRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{2,63}$`)

func (s *Store) ClientAccount(deviceID string) (model.ClientAccount, error) {
	var result model.ClientAccount
	err := s.DB.QueryRow("SELECT username FROM device_accounts WHERE device_id=?", deviceID).Scan(&result.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	result.Initialized = err == nil
	return result, err
}
func (s *Store) SetClientAccount(deviceID, username, password string) error {
	username = strings.TrimSpace(username)
	if !accountRE.MatchString(username) {
		return errors.New("设备账号须为 3–64 位字母、数字或 . _ @ -")
	}
	if len([]rune(password)) < 12 || len(password) > 1024 {
		return errors.New("设备密码须至少 12 个字符")
	}
	if _, err := s.Device(deviceID); err != nil {
		return err
	}
	hash := secure.HashPassword(password)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRow("SELECT COUNT(*) FROM devices WHERE id=? AND deleted_at=0", deviceID).Scan(&active); err != nil {
		return err
	}
	if active != 1 {
		return ErrNotFound
	}
	if _, err = tx.Exec("INSERT INTO device_accounts(device_id,username,password) VALUES(?,?,?) ON CONFLICT(device_id) DO UPDATE SET username=excluded.username,password=excluded.password", deviceID, username, hash); err != nil {
		return errors.New("账号已由其他设备使用")
	}
	if _, err = tx.Exec("UPDATE client_sessions SET revoked=1 WHERE device_id=?", deviceID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.Audit("device.account_reset", deviceID)
	return nil
}
func (s *Store) ClientLogin(username, password, installationID, clientName string) (model.ClientTokens, error) {
	var result model.ClientTokens
	var id, hash string
	var enabled bool
	err := s.DB.QueryRow("SELECT a.device_id,a.password,d.enabled FROM device_accounts a JOIN devices d ON d.id=a.device_id WHERE a.username=? COLLATE NOCASE", username).Scan(&id, &hash, &enabled)
	if err != nil {
		return result, ErrUnauthorized
	}
	if !secure.CheckPassword(hash, password) || !enabled {
		return result, ErrUnauthorized
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var currentHash string
	var currentEnabled bool
	if err = tx.QueryRow("SELECT a.password,d.enabled FROM device_accounts a JOIN devices d ON d.id=a.device_id WHERE a.device_id=?", id).Scan(&currentHash, &currentEnabled); err != nil || !currentEnabled || currentHash != hash {
		return result, ErrUnauthorized
	}
	if _, err = tx.Exec("UPDATE client_sessions SET revoked=1 WHERE device_id=? AND installation_id=?", id, installationID); err != nil {
		return result, err
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM client_sessions WHERE device_id=? AND revoked=0 AND refresh_expires>?", id, s.Now().Unix()).Scan(&count); err != nil {
		return result, err
	}
	if count >= 32 {
		return result, errors.New("此设备已达到 32 个客户端会话上限")
	}
	result.AccessToken = secure.Token()
	result.RefreshToken = secure.Token()
	result.SessionID = NewID()
	result.AccessExpiresAt = s.Now().Add(15 * time.Minute).UTC()
	result.RefreshExpiresAt = s.Now().Add(30 * 24 * time.Hour).UTC()
	result.Device.ID = id
	if err = tx.QueryRow("SELECT name FROM devices WHERE id=?", id).Scan(&result.Device.Name); err != nil {
		return result, err
	}
	_, err = tx.Exec("INSERT INTO client_sessions(id,device_id,installation_id,client_name,access_hash,refresh_hash,access_expires,refresh_expires,last_seen,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", result.SessionID, id, installationID, clientName, secure.Hash(result.AccessToken), secure.Hash(result.RefreshToken), result.AccessExpiresAt.Unix(), result.RefreshExpiresAt.Unix(), s.Now().Unix(), s.Now().Unix())
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) ClientRefresh(token string) (model.ClientTokens, error) {
	var result model.ClientTokens
	tx, err := s.DB.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	digest := secure.Hash(token)
	var replaySession string
	if err = tx.QueryRow("SELECT session_id FROM spent_refresh WHERE hash=? AND expires>?", digest, s.Now().Unix()).Scan(&replaySession); err == nil {
		_, _ = tx.Exec("UPDATE client_sessions SET revoked=1 WHERE id=?", replaySession)
		_ = tx.Commit()
		return result, ErrUnauthorized
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var expires int64
	err = tx.QueryRow("SELECT cs.id,cs.device_id,d.name,cs.refresh_expires FROM client_sessions cs JOIN devices d ON d.id=cs.device_id WHERE cs.refresh_hash=? AND cs.refresh_expires>? AND cs.revoked=0 AND d.enabled=1", digest, s.Now().Unix()).Scan(&result.SessionID, &result.Device.ID, &result.Device.Name, &expires)
	if err != nil {
		return result, ErrUnauthorized
	}
	result.AccessToken = secure.Token()
	result.RefreshToken = secure.Token()
	result.AccessExpiresAt = s.Now().Add(15 * time.Minute).UTC()
	result.RefreshExpiresAt = time.Unix(expires, 0).UTC()
	if _, err = tx.Exec("INSERT INTO spent_refresh(hash,session_id,expires) VALUES(?,?,?)", digest, result.SessionID, expires); err != nil {
		return result, err
	}
	if _, err = tx.Exec("UPDATE client_sessions SET access_hash=?,refresh_hash=?,access_expires=?,last_seen=? WHERE id=?", secure.Hash(result.AccessToken), secure.Hash(result.RefreshToken), result.AccessExpiresAt.Unix(), s.Now().Unix(), result.SessionID); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) ClientIdentity(token string) (deviceID, sessionID string, err error) {
	err = s.DB.QueryRow("SELECT cs.device_id,cs.id FROM client_sessions cs JOIN devices d ON d.id=cs.device_id WHERE cs.access_hash=? AND cs.access_expires>? AND cs.revoked=0 AND d.enabled=1", secure.Hash(token), s.Now().Unix()).Scan(&deviceID, &sessionID)
	if err != nil {
		err = ErrUnauthorized
	}
	return
}
func (s *Store) ClientLogout(sessionID string) error {
	_, err := s.DB.Exec("UPDATE client_sessions SET revoked=1 WHERE id=?", sessionID)
	return err
}
func (s *Store) ClientSessions(deviceID string) ([]model.ClientSession, error) {
	a, err := s.Assignment(deviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query("SELECT id,installation_id,client_name,last_seen,status FROM client_sessions WHERE device_id=? AND revoked=0 AND refresh_expires>? ORDER BY last_seen DESC", deviceID, s.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.ClientSession{}
	for rows.Next() {
		var c model.ClientSession
		var raw string
		var last int64
		if err = rows.Scan(&c.ID, &c.InstallationID, &c.ClientName, &last, &raw); err != nil {
			return nil, err
		}
		var status model.ClientStatus
		_ = json.Unmarshal([]byte(raw), &status)
		c.CoreVersion = status.CoreVersion
		c.DesiredNodeID = status.DesiredNodeID
		c.ActualNodeID = status.ActualNodeID
		c.RunState = status.RunState
		c.Mode = status.Mode
		c.TUNEnabled = status.TUNEnabled
		c.ConnectionIntent = status.ConnectionIntent
		c.AppliedConfigRevision = status.AppliedConfigRevision
		c.ProcessedConfigRevision = status.ProcessedConfigRevision
		c.DesiredConfigRevision = a.Revision
		c.RevocationPending = status.AppliedConfigRevision < a.Revision
		c.LastError = status.LastError
		t := time.Unix(last, 0).UTC()
		c.LastSeen = &t
		result = append(result, c)
	}
	return result, rows.Err()
}
func (s *Store) ClientStatus(deviceID, sessionID string, status model.ClientStatus) error {
	if status.AppliedConfigRevision < 0 || status.ProcessedConfigRevision < 0 || len(status.LastError) > 1000 || len(status.CoreVersion) > 80 {
		return errors.New("客户端状态格式无效")
	}
	switch status.RunState {
	case "disconnected", "connecting", "connected", "failed", "cached":
	default:
		return errors.New("客户端运行状态无效")
	}
	switch status.Mode {
	case "", "rule", "global", "direct":
	default:
		return errors.New("客户端模式无效")
	}
	a, err := s.Assignment(deviceID)
	if err != nil {
		return err
	}
	if status.AppliedConfigRevision > a.Revision || status.ProcessedConfigRevision > a.Revision {
		return ErrConflict
	}
	if status.ProcessedConfigRevision < status.AppliedConfigRevision {
		status.ProcessedConfigRevision = status.AppliedConfigRevision
	}
	allowed := func(id string) bool {
		if id == "" {
			return true
		}
		for _, n := range a.NodeIDs {
			if n == id {
				return true
			}
		}
		return false
	}
	if status.AppliedConfigRevision == a.Revision && (!allowed(status.DesiredNodeID) || !allowed(status.ActualNodeID)) {
		return errors.New("客户端报告了未分配的节点")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE client_sessions SET status=?,last_seen=? WHERE id=? AND device_id=? AND revoked=0", jsonString(status), s.Now().Unix(), sessionID, deviceID)
	if err != nil {
		return err
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		return ErrUnauthorized
	}
	if err = s.settleRemovals(tx, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ClientProfile(deviceID string) (model.ClientProfile, error) {
	var p model.ClientProfile
	d, err := s.Device(deviceID)
	if err != nil {
		return p, err
	}
	a, err := s.Assignment(deviceID)
	if err != nil {
		return p, err
	}
	p.DeviceID = d.ID
	p.Name = d.Name
	p.Enabled = d.Enabled
	p.Revision = a.Revision
	p.DefaultNodeID = a.DefaultNodeID
	p.SubscriptionURL = d.SubscriptionURL
	p.Nodes = []model.ClientNode{}
	for _, id := range a.NodeIDs {
		n, e := s.Node(id)
		if e != nil {
			return p, e
		}
		pass, e := s.Credential(id, deviceID)
		if e != nil {
			return p, e
		}
		var required int64
		if e = s.DB.QueryRow("SELECT required_revision FROM assignments WHERE device_id=? AND node_id=?", deviceID, id).Scan(&required); e != nil {
			return p, e
		}
		if n.Lifecycle != "active" {
			return p, ErrConflict
		}
		p.Nodes = append(p.Nodes, model.ClientNode{ID: n.ID, Name: n.Name, Server: n.Domain, PublicIP: n.PublicIP, Port: n.Port, Password: pass, SNI: n.Domain, Ready: n.Status == "ready" && n.AppliedRevision >= required})
	}
	var hash string
	var version int64
	err = s.DB.QueryRow("SELECT hash,version FROM rule_versions ORDER BY version DESC LIMIT 1").Scan(&hash, &version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.Rules = model.RuleReference{Version: version, Hash: hash, URL: s.BaseURL + "/api/v1/client/rules/" + hash}
	var revision int64
	if err = s.DB.QueryRow("SELECT revision FROM device_config WHERE device_id=?", deviceID).Scan(&revision); err != nil {
		return p, err
	}
	if revision != p.Revision {
		return p, ErrConflict
	}
	return p, nil
}

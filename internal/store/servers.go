package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func (s *Store) ControllerID() (string, error) {
	var id string
	err := s.DB.QueryRow("SELECT value FROM metadata WHERE key='controller_id'").Scan(&id)
	return id, err
}

func validServer(v model.Server) error {
	if err := validServerSettings(v.Name, nil, nil); err != nil {
		return err
	}
	if strings.TrimSpace(v.SSHHost) == "" || len(v.SSHHost) > 253 || strings.ContainsAny(v.SSHHost, "\r\n\x00 /\\") || v.SSHPort < 1 || v.SSHPort > 65535 || v.SSHUser == "" || strings.ContainsAny(v.SSHUser, "\r\n\x00 /\\") {
		return errors.New("服务器名称或 SSH 连接参数无效")
	}
	return nil
}
func (s *Store) CreateServer(v model.Server, actor ...string) (model.Server, error) {
	return s.createServer(v, "", actor...)
}
func (s *Store) CreateServerWithCredential(v model.Server, password string, actor ...string) (model.Server, error) {
	return s.createServer(v, password, actor...)
}
func (s *Store) createServer(v model.Server, password string, actor ...string) (model.Server, error) {
	if v.SSHPort == 0 {
		v.SSHPort = 22
	}
	if v.SSHUser == "" {
		v.SSHUser = "root"
	}
	if err := validServer(v); err != nil {
		return v, err
	}
	if err := validServerSettings(v.Name, v.BandwidthMbps, v.MonthlyQuotaBytes); err != nil {
		return v, err
	}
	v.ID = NewID()
	v.Revision = 1
	v.CreatedAt = s.Now().UTC()
	v.Addresses = []model.ServerAddress{}
	config, err := serverConfigJSON(v)
	if err != nil {
		return v, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	// A verified machine cannot be registered twice under different addresses.
	if v.MachineID != "" && v.HostKey != "" {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM servers WHERE deleted_at=0 AND json_extract(config,'$.machineId')=? AND json_extract(config,'$.hostKey')=?", v.MachineID, v.HostKey).Scan(&count); err != nil {
			return v, err
		}
		if count > 0 {
			return v, errors.New("该主机已经登记，请在已有服务器中管理 IP")
		}
	}
	_, err = tx.Exec("INSERT INTO servers(id,config) VALUES(?,?)", v.ID, config)
	if err != nil {
		return v, err
	}
	if password != "" {
		if err = s.saveServerCredential(tx, v, password); err != nil {
			return v, err
		}
	}
	if v.Billing.ExpiresAt != nil {
		if err = setupServerBilling(tx, v.ID, *v.Billing.ExpiresAt, v.Revision, billingActor(actor), s.Now()); err != nil {
			return v, err
		}
	}
	err = tx.Commit()
	if err == nil {
		s.Audit("server.create", v.ID)
	}
	if err != nil {
		return v, err
	}
	return s.Server(v.ID)
}
func (s *Store) Server(id string) (model.Server, error) {
	var v model.Server
	var raw string
	err := s.DB.QueryRow("SELECT config,revision FROM servers WHERE id=? AND deleted_at=0", id).Scan(&raw, &v.Revision)
	if err != nil {
		return v, notFound(err)
	}
	rev := v.Revision
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return v, err
	}
	v.Revision = rev
	v.Addresses, err = s.Addresses(id)
	if err != nil {
		return v, err
	}
	err = s.serverMonitoring(&v)
	if err == nil {
		v.Billing, err = BillingInSnapshot(s.DB, id, "", s.Now())
	}
	return v, err
}
func (s *Store) Servers() ([]model.Server, error) {
	rows, err := s.DB.Query("SELECT id FROM servers WHERE deleted_at=0 ORDER BY json_extract(config,'$.createdAt') DESC,id")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []model.Server{}
	for _, id := range ids {
		v, e := s.Server(id)
		if e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, nil
}
func (s *Store) UpdateServer(v model.Server, expected int64, password ...string) (model.Server, error) {
	if err := validServer(v); err != nil {
		return v, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	var raw string
	var rev int64
	if err = tx.QueryRow("SELECT config,revision FROM servers WHERE id=? AND deleted_at=0", v.ID).Scan(&raw, &rev); err != nil {
		return v, notFound(err)
	}
	if rev != expected {
		return v, ErrConflict
	}
	if err = ensureServerIdle(tx, v.ID); err != nil {
		return v, err
	}
	var old model.Server
	if err = json.Unmarshal([]byte(raw), &old); err != nil {
		return v, err
	}
	if old.HostKey != "" && v.HostKey != old.HostKey || old.MachineID != "" && v.MachineID != old.MachineID {
		return v, errors.New("SSH 主机身份发生变化，已拒绝修改")
	}
	v.CreatedAt = old.CreatedAt
	v.Revision = rev + 1
	// SSH edits must preserve the independently managed capacity settings.
	v.BandwidthMbps = old.BandwidthMbps
	v.MonthlyQuotaBytes = old.MonthlyQuotaBytes
	config, err := serverConfigJSON(v)
	if err != nil {
		return v, err
	}
	if _, err = tx.Exec("UPDATE servers SET config=?,revision=? WHERE id=?", config, v.Revision, v.ID); err != nil {
		return v, err
	}
	if _, err = tx.Exec("UPDATE nodes SET config=json_set(config,'$.sshHost',?,'$.sshPort',?,'$.sshUser',?,'$.hostKey',?,'$.machineId',?) WHERE json_extract(config,'$.serverId')=? AND lifecycle!='deleted'", v.SSHHost, v.SSHPort, v.SSHUser, v.HostKey, v.MachineID, v.ID); err != nil {
		return v, err
	}
	if len(password) > 0 && password[0] != "" {
		if err = s.saveServerCredential(tx, v, password[0]); err != nil {
			return v, err
		}
	}
	if err = tx.Commit(); err != nil {
		return v, err
	}
	s.Audit("server.update", v.ID)
	return s.Server(v.ID)
}
func (s *Store) PinServer(id, fingerprint, machineID string) error {
	if fingerprint == "" {
		return errors.New("SSH 主机指纹不能为空")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE servers SET config=json_set(config,'$.hostKey',?,'$.machineId',CASE WHEN ?='' THEN COALESCE(json_extract(config,'$.machineId'),'') ELSE ? END) WHERE id=? AND deleted_at=0 AND (COALESCE(json_extract(config,'$.hostKey'),'')='' OR json_extract(config,'$.hostKey')=?) AND (?='' OR COALESCE(json_extract(config,'$.machineId'),'')='' OR json_extract(config,'$.machineId')=?)", fingerprint, machineID, machineID, id, fingerprint, machineID, machineID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("服务器不存在或 SSH 主机身份发生变化")
	}
	if _, err = tx.Exec("UPDATE nodes SET config=json_set(config,'$.hostKey',?,'$.machineId',CASE WHEN ?='' THEN COALESCE(json_extract(config,'$.machineId'),'') ELSE ? END) WHERE json_extract(config,'$.serverId')=? AND lifecycle!='deleted'", fingerprint, machineID, machineID, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DeleteServer(id string, expected int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = ensureServerIdle(tx, id); err != nil {
		return err
	}
	var rev int64
	if err = tx.QueryRow("SELECT revision FROM servers WHERE id=? AND deleted_at=0", id).Scan(&rev); err != nil {
		return notFound(err)
	}
	if rev != expected {
		return ErrConflict
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE json_extract(config,'$.serverId')=? AND lifecycle!='deleted'", id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("请先完成服务器下全部节点的删除和清理")
	}
	if _, err = tx.Exec("UPDATE servers SET deleted_at=?,revision=revision+1 WHERE id=?", s.Now().Unix(), id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM server_credentials WHERE server_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE server_addresses SET deleted_at=? WHERE server_id=? AND deleted_at=0", s.Now().Unix(), id); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.Audit("server.delete", id)
	}
	return err
}
func (s *Store) Addresses(serverID string) ([]model.ServerAddress, error) {
	rows, err := s.DB.Query("SELECT id,ip,created_at FROM server_addresses WHERE server_id=? AND deleted_at=0 ORDER BY created_at,id", serverID)
	if err != nil {
		return nil, err
	}
	result := []model.ServerAddress{}
	for rows.Next() {
		v := model.ServerAddress{ServerID: serverID, NodeIDs: []string{}}
		var at int64
		if err = rows.Scan(&v.ID, &v.IP, &at); err != nil {
			rows.Close()
			return nil, err
		}
		v.CreatedAt = time.Unix(at, 0).UTC()
		result = append(result, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		rows, err = s.DB.Query("SELECT id FROM nodes WHERE json_extract(config,'$.addressId')=? AND lifecycle!='deleted' ORDER BY id", result[i].ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			result[i].NodeIDs = append(result[i].NodeIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (s *Store) CreateAddress(serverID, ip string) (model.ServerAddress, error) {
	v := model.ServerAddress{ID: NewID(), ServerID: serverID, IP: ip, CreatedAt: s.Now().UTC(), NodeIDs: []string{}}
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() || !a.IsGlobalUnicast() || a.IsPrivate() {
		return v, errors.New("仅支持直接绑定网卡的公网 IPv4")
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4"} {
		if netip.MustParsePrefix(prefix).Contains(a) {
			return v, errors.New("该地址不是可用公网 IPv4")
		}
	}
	res, err := s.DB.Exec("INSERT INTO server_addresses(id,server_id,ip,created_at) SELECT ?,id,?,? FROM servers WHERE id=? AND deleted_at=0", v.ID, a.String(), s.Now().Unix(), serverID)
	if err != nil {
		return v, errors.New("该 IP 已在服务器中登记")
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		return v, ErrNotFound
	}
	s.Audit("server.address.create", v.ID)
	return v, nil
}
func (s *Store) DeleteAddress(serverID, addressID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE json_extract(config,'$.addressId')=? AND lifecycle!='deleted'", addressID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("该 IP 仍有节点或待清理实例，请先完成节点删除")
	}
	res, err := tx.Exec("UPDATE server_addresses SET deleted_at=? WHERE id=? AND server_id=? AND deleted_at=0", s.Now().Unix(), addressID, serverID)
	if err != nil {
		return err
	}
	count64, _ := res.RowsAffected()
	if count64 != 1 {
		return ErrNotFound
	}
	if err = tx.Commit(); err == nil {
		s.Audit("server.address.delete", addressID)
	}
	return err
}

// prepareNode resolves authoritative SSH/address data and allocates local ports
// while the create transaction serializes all nodes on this server.
func (s *Store) prepareNode(tx *sql.Tx, n *model.Node) error {
	if n.ServerID == "" {
		// Compatibility for existing API callers and fixtures. Never merge an
		// unverified SSH address with an existing physical server implicitly.
		v := model.Server{ID: NewID(), Name: n.Name, SSHHost: n.SSHHost, SSHPort: n.SSHPort, SSHUser: n.SSHUser, HostKey: n.HostKey, MachineID: n.MachineID, Revision: 1, CreatedAt: s.Now().UTC()}
		if _, err := tx.Exec("INSERT INTO servers(id,config) VALUES(?,?)", v.ID, jsonString(v)); err != nil {
			return err
		}
		n.ServerID = v.ID
		n.AddressID = NewID()
		if _, err := tx.Exec("INSERT INTO server_addresses(id,server_id,ip,created_at) VALUES(?,?,?,?)", n.AddressID, n.ServerID, n.PublicIP, s.Now().Unix()); err != nil {
			return err
		}
	} else {
		var raw string
		var ip string
		if err := tx.QueryRow("SELECT s.config,a.ip FROM servers s JOIN server_addresses a ON a.server_id=s.id WHERE s.id=? AND a.id=? AND s.deleted_at=0 AND a.deleted_at=0", n.ServerID, n.AddressID).Scan(&raw, &ip); err != nil {
			return notFound(err)
		}
		var v model.Server
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return err
		}
		n.SSHHost = v.SSHHost
		n.SSHPort = v.SSHPort
		n.SSHUser = v.SSHUser
		n.HostKey = v.HostKey
		n.MachineID = v.MachineID
		n.PublicIP = ip
	}
	n.AuthPort = 20000
	n.StatsPort = 20001
	for ; n.StatsPort < 60000; n.AuthPort, n.StatsPort = n.AuthPort+2, n.StatsPort+2 {
		var used int
		if err := tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE json_extract(config,'$.serverId')=? AND lifecycle!='deleted' AND (json_extract(config,'$.authPort') IN (?,?) OR json_extract(config,'$.statsPort') IN (?,?))", n.ServerID, n.AuthPort, n.StatsPort, n.AuthPort, n.StatsPort).Scan(&used); err != nil {
			return err
		}
		if used == 0 {
			return nil
		}
	}
	return errors.New("服务器本地鉴权端口已耗尽")
}

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"time"
)

func (s *Store) CreateNode(n model.Node, password string) (model.Node, error) {
	// Node capacity fields are deprecated; traffic remains in the usage ledger.
	n.BandwidthMbps, n.MonthlyQuotaBytes = nil, nil
	n.ID = NewID()
	n.CreatedAt = s.Now().UTC()
	n.Status = "pending"
	n.Message = "等待部署"
	n.DesiredRevision = 1
	n.Revision = 1
	n.Lifecycle = "active"
	n.Managed = false
	n.DeletedAt = nil
	n.InstallationVersion = 2
	if n.CertificateMode == "" {
		n.CertificateMode = "http"
	}
	if n.DNSMode == "" {
		n.DNSMode = "manual"
	}
	if n.DNSMode == "cloudflare" {
		n.CertificateMode = "cloudflare"
	}
	if n.CertificateMode != "http" && n.CertificateMode != "cloudflare" || n.DNSMode != "manual" && n.DNSMode != "cloudflare" {
		return n, errors.New("域名或证书模式无效")
	}
	pass, err := s.Vault.Encrypt(password)
	if err != nil {
		return n, err
	}
	agent := secure.Token()
	agentEnc, err := s.Vault.Encrypt(agent)
	if err != nil {
		return n, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return n, err
	}
	defer tx.Rollback()
	if err = s.prepareNode(tx, &n); err != nil {
		return n, err
	}
	var exists int
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE lifecycle!='deleted' AND ((?!='' AND lower(json_extract(config,'$.domain'))=lower(?)) OR (json_extract(config,'$.serverId')=? AND json_extract(config,'$.addressId')=?) OR (lower(json_extract(config,'$.sshHost'))=lower(?) AND json_extract(config,'$.publicIp')=? AND json_extract(config,'$.port')=?))", n.Domain, n.Domain, n.ServerID, n.AddressID, n.SSHHost, n.PublicIP, n.Port).Scan(&exists); err != nil {
		return n, err
	}
	if exists > 0 {
		return n, errors.New("该域名、服务器 IP 或监听端口已添加，请在原节点重试")
	}
	if _, err = tx.Exec("INSERT INTO nodes(id,config,password,password_expires,agent_hash,agent_secret) VALUES(?,?,?,?,?,?)", n.ID, jsonString(n), pass, s.Now().Add(24*time.Hour).Unix(), secure.Hash(agent), agentEnc); err != nil {
		return n, err
	}
	ids := []string{ProbeID(n.ID)}
	for _, id := range ids {
		enc, e := s.Vault.Encrypt(secure.Token())
		if e != nil {
			return n, e
		}
		if _, err = tx.Exec("INSERT INTO credentials(node_id,device_id,password) VALUES(?,?,?)", n.ID, id, enc); err != nil {
			return n, err
		}
	}
	if err = tx.QueryRow("INSERT INTO jobs(node_id,kind,created_at) VALUES(?,'install',?) RETURNING id", n.ID, s.Now().Unix()).Scan(&n.QueuedJobID); err != nil {
		return n, err
	}
	if err = tx.Commit(); err != nil {
		return n, err
	}
	s.Audit("node.create", n.ID)
	return n, nil
}

func (s *Store) Node(id string) (model.Node, error) {
	var n model.Node
	var raw, metrics, online, caps, cert string
	var last, deleted int64
	var desired, applied int64
	var healthy bool
	var lifecycle string
	var revision int64
	var managed bool
	err := s.DB.QueryRow("SELECT config,desired_revision,applied_revision,last_seen,metrics,online,core_healthy,lifecycle,revision,managed,deleted_at,capabilities,certificate_status FROM nodes WHERE id=?", id).Scan(&raw, &desired, &applied, &last, &metrics, &online, &healthy, &lifecycle, &revision, &managed, &deleted, &caps, &cert)
	if err != nil {
		return n, notFound(err)
	}
	if err = json.Unmarshal([]byte(raw), &n); err != nil {
		return n, err
	}
	// Old records may contain advisory limits. Never expose or apply them again.
	n.BandwidthMbps, n.MonthlyQuotaBytes = nil, nil
	_ = json.Unmarshal([]byte(metrics), &n.Metrics)
	n.Lifecycle = lifecycle
	n.Revision = revision
	n.Managed = managed
	n.Capabilities = []string{}
	_ = json.Unmarshal([]byte(caps), &n.Capabilities)
	if cert != "{}" {
		n.Certificate = &model.CertificateStatus{}
		_ = json.Unmarshal([]byte(cert), n.Certificate)
	}
	if deleted > 0 {
		t := time.Unix(deleted, 0).UTC()
		n.DeletedAt = &t
	}
	var retirement model.RetirementStatus
	err = s.DB.QueryRow("SELECT id,stopped,final FROM node_retirements WHERE node_id=?", id).Scan(&retirement.ID, &retirement.Stopped, &retirement.Final)
	if err != nil && err != sql.ErrNoRows {
		return n, err
	}
	if err == nil {
		n.Retirement = &retirement
	}
	n.DesiredRevision = desired
	n.AppliedRevision = applied
	n.CoreHealthy = healthy
	if last > 0 {
		t := time.Unix(last, 0).UTC()
		n.LastSeen = &t
	}
	n.OnlineDevices = 0
	n.Instances = 0
	if healthy && last > s.Now().Add(-30*time.Second).Unix() {
		var active map[string]int
		_ = json.Unmarshal([]byte(online), &active)
		for id, count := range active {
			if len(id) > 8 && id[:8] == "__probe_" {
				continue
			}
			if count > 0 {
				n.OnlineDevices++
				n.Instances += count
			}
		}
	} else {
		n.Metrics.UploadRate = 0
		n.Metrics.DownloadRate = 0
	}
	err = s.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM daily WHERE node_id=? AND start>=?", id, MonthStart(s.Now())).Scan(&n.MonthUpload, &n.MonthDownload)
	if err != nil {
		return n, err
	}
	err = s.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM lifetime WHERE node_id=?", id).Scan(&n.TotalUpload, &n.TotalDownload)
	if err == nil {
		n.Billing, err = BillingInSnapshot(s.DB, n.ServerID, id, s.Now())
	}
	return n, err
}

func (s *Store) Nodes() ([]model.Node, error) {
	rows, err := s.DB.Query("SELECT id FROM nodes WHERE lifecycle!='deleted' ORDER BY json_extract(config,'$.createdAt') DESC")
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
	nodes := []model.Node{}
	for _, id := range ids {
		n, e := s.Node(id)
		if e != nil {
			return nil, e
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (s *Store) SaveNode(n model.Node) error {
	// Administrator edits own only the name and discard obsolete node capacities.
	// Never replace a stale snapshot over a worker's host-key pin or deployment
	// state transition.
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.name',?,'$.bandwidthMbps',NULL,'$.monthlyQuotaBytes',NULL),revision=revision+1 WHERE id=? AND lifecycle='active' AND revision=?", n.Name, n.ID, n.Revision)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrConflict
	}
	return nil
}
func (s *Store) SetStatus(id, status, message string, shared bool) error {
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.status',?,'$.message',?,'$.sharedHost',json(?)) WHERE id=? AND lifecycle='active'", status, message, jsonString(shared), id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetTopology(id string, controlOnNode, acmeViaCaddy bool) error {
	_, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.controlOnNode',json(?),'$.acmeViaCaddy',json(?),'$.sharedHost',json(?)) WHERE id=? AND lifecycle='active'", jsonString(controlOnNode), jsonString(acmeViaCaddy), jsonString(acmeViaCaddy), id)
	return err
}
func (s *Store) Pin(id, fingerprint string) error {
	if fingerprint == "" {
		return errors.New("SSH 主机指纹不能为空")
	}
	n, err := s.Node(id)
	if err != nil {
		return err
	}
	if n.ServerID != "" {
		if err = s.PinServer(n.ServerID, fingerprint, n.MachineID); err != nil {
			return err
		}
	}
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.hostKey',?) WHERE id=? AND (COALESCE(json_extract(config,'$.hostKey'),'')='' OR json_extract(config,'$.hostKey')=?)", fingerprint, id, fingerprint)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		if _, err = s.Node(id); err != nil {
			return err
		}
		return errors.New("SSH 主机指纹发生变化，已拒绝连接")
	}
	return nil
}

func (s *Store) ExternalVerified(id string) error {
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.status','ready','$.message','外部 Mihomo 验证通过') WHERE id=? AND lifecycle='active' AND json_extract(config,'$.status') IN ('awaiting_external','ready')", id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return errors.New("节点状态已变化，请等待当前任务完成后重新验证")
	}
	return nil
}

func (s *Store) NodeSecrets(id string) (password, agent, probe string, err error) {
	var p, a, pr string
	var expires int64
	err = s.DB.QueryRow("SELECT password,password_expires,agent_secret FROM nodes WHERE id=?", id).Scan(&p, &expires, &a)
	if err != nil {
		return "", "", "", notFound(err)
	}
	if expires > s.Now().Unix() {
		password, err = s.Vault.Decrypt(p)
		if err != nil {
			return "", "", "", secretError(err)
		}
	}
	agent, err = s.Vault.Decrypt(a)
	if err != nil {
		return "", "", "", secretError(err)
	}
	err = s.DB.QueryRow("SELECT password FROM credentials WHERE node_id=? AND device_id=?", id, ProbeID(id)).Scan(&pr)
	if err != nil {
		return "", "", "", err
	}
	probe, err = s.Vault.Decrypt(pr)
	return password, agent, probe, secretError(err)
}
func (s *Store) ClearSSHPassword(id string) {
	_, _ = s.DB.Exec("UPDATE nodes SET password='',password_expires=0 WHERE id=?", id)
}
func (s *Store) ResolveAgent(token string) (string, error) {
	var id string
	err := s.DB.QueryRow("SELECT id FROM nodes WHERE agent_hash=?", secure.Hash(token)).Scan(&id)
	return id, notFound(err)
}

var _ = sql.ErrNoRows

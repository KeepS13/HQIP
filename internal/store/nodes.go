package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func (s *Store) CreateNode(n model.Node, password string) (model.Node, error) {
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
	if _, err = tx.Exec("INSERT INTO jobs(node_id,kind,created_at) VALUES(?,'install',?)", n.ID, s.Now().Unix()); err != nil {
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
	// Administrator edits own only these three fields. Never replace a stale
	// snapshot over a worker's host-key pin or deployment state transition.
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.name',?,'$.bandwidthMbps',?,'$.monthlyQuotaBytes',?),revision=revision+1 WHERE id=? AND lifecycle='active' AND revision=?", n.Name, n.BandwidthMbps, n.MonthlyQuotaBytes, n.ID, n.Revision)
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

func (s *Store) Desired(id string) (model.AgentDesired, error) {
	var result model.AgentDesired
	result.PortalEnabled = s.PortalEnabled()
	result.Devices = []model.AgentDevice{}
	var lifecycle string
	err := s.DB.QueryRow("SELECT desired_revision,lifecycle FROM nodes WHERE id=?", id).Scan(&result.Revision, &lifecycle)
	if err != nil {
		return result, notFound(err)
	}
	var retirement model.AgentRetirement
	err = s.DB.QueryRow("SELECT id,stopped FROM node_retirements WHERE node_id=?", id).Scan(&retirement.ID, &retirement.Finalize)
	if err == nil {
		result.Retirement = &retirement
	} else if err != sql.ErrNoRows {
		return result, err
	}
	rows, err := s.DB.Query(`SELECT c.device_id,c.password,
CASE WHEN ?!='active' THEN 0 WHEN c.device_id=? THEN 1 WHEN d.id IS NULL OR d.deleted_at>0 OR d.enabled=0 THEN 0 WHEN a.node_id IS NOT NULL THEN 1 WHEN r.id IS NOT NULL AND r.immediate=0 AND r.status!='revoked' THEN 1 ELSE 0 END,
COALESCE(r.id,''),CASE WHEN r.id IS NOT NULL AND r.immediate=0 AND r.status!='revoked' THEN 60 ELSE 0 END
FROM credentials c LEFT JOIN devices d ON d.id=c.device_id
LEFT JOIN assignments a ON a.device_id=c.device_id AND a.node_id=c.node_id
LEFT JOIN removals r ON r.id=(SELECT rr.id FROM removals rr WHERE rr.device_id=c.device_id AND rr.node_id=c.node_id AND rr.status!='cancelled' ORDER BY rr.created_at DESC,rr.rowid DESC LIMIT 1) AND a.node_id IS NULL
WHERE c.node_id=? ORDER BY c.device_id`, lifecycle, ProbeID(id), id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var d model.AgentDevice
		var enc string
		if err = rows.Scan(&d.ID, &enc, &d.Enabled, &d.RemovalID, &d.RevokeAfterSeconds); err != nil {
			return result, err
		}
		d.Password, err = s.Vault.Decrypt(enc)
		if err != nil {
			return result, secretError(err)
		}
		result.Devices = append(result.Devices, d)
		if result.PortalEnabled && d.ID != ProbeID(id) {
			result.Devices[len(result.Devices)-1].Enabled = false
			result.Devices[len(result.Devices)-1].RevokeAfterSeconds = 0
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if lifecycle == "active" {
		var mode string
		if err = s.DB.QueryRow("SELECT COALESCE(json_extract(config,'$.certificateMode'),'http') FROM nodes WHERE id=?", id).Scan(&mode); err != nil {
			return result, err
		}
		if mode == "cloudflare" {
			result.Certificate, err = s.CertificateBundle(id)
		}
	}
	return result, err
}

type Job struct {
	ID                   int64
	NodeID, Kind         string
	Takeover             bool   `json:"takeover,omitempty"`
	PreflightFingerprint string `json:"preflightFingerprint,omitempty"`
	CertificateMode      string `json:"certificateMode,omitempty"`
	ZoneID               string `json:"zoneId,omitempty"`
}

func (s *Store) RecoverJobs() error {
	_, err := s.DB.Exec("UPDATE jobs SET state='pending' WHERE state='running'; UPDATE jobs SET state='cancelled' WHERE state='cancelling'")
	return err
}
func (s *Store) NextJob() (Job, error) {
	var j Job
	tx, err := s.DB.Begin()
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	var options string
	err = tx.QueryRow(`SELECT j.id,j.node_id,j.kind,j.options FROM jobs j JOIN nodes n ON n.id=j.node_id
WHERE j.state='pending' AND ((n.lifecycle='active' AND j.kind!='retire') OR (n.lifecycle='retiring' AND j.kind='retire'))
AND NOT EXISTS(SELECT 1 FROM jobs busy JOIN nodes bn ON bn.id=busy.node_id WHERE busy.state IN ('running','cancelling') AND json_extract(bn.config,'$.serverId')=json_extract(n.config,'$.serverId')) ORDER BY j.id LIMIT 1`).Scan(&j.ID, &j.NodeID, &j.Kind, &options)
	if err != nil {
		return j, notFound(err)
	}
	var opts Job
	if err = json.Unmarshal([]byte(options), &opts); err != nil {
		return j, err
	}
	j.Takeover = opts.Takeover
	j.PreflightFingerprint = opts.PreflightFingerprint
	j.CertificateMode = opts.CertificateMode
	j.ZoneID = opts.ZoneID
	if _, err = tx.Exec("UPDATE jobs SET state='running',started_at=? WHERE id=?", s.Now().Unix(), j.ID); err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) FinishJob(j Job, success bool) {
	state := "error"
	if success {
		state = "done"
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return
	}
	err = tx.QueryRow("UPDATE jobs SET state=CASE WHEN state='cancelling' THEN 'cancelled' ELSE ? END WHERE id=? AND state IN ('running','cancelling') RETURNING state", state, j.ID).Scan(&state)
	if err == nil && state == "error" {
		err = recordJobNotification(tx, j.ID, s.Now().Unix())
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	// If no owner was ever installed, cancellation has no remote resources to
	// retire. Managed partial installs remain visible until stopped/cleaned.
	var abandoned int
	if s.DB.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=? AND lifecycle='retiring' AND managed=0 AND NOT EXISTS(SELECT 1 FROM jobs WHERE node_id=? AND state IN ('running','cancelling'))", j.NodeID, j.NodeID).Scan(&abandoned) == nil && abandoned == 1 {
		_ = s.CompleteNodeRetirement(j.NodeID)
	}
}
func (s *Store) Queue(id, kind, password string) (model.Node, error) {
	if kind != "install" && kind != "verify" {
		return model.Node{}, errors.New("任务类型无效")
	}
	return s.queueWithOptions(id, kind, password, Job{})
}

func (s *Store) CheckIntegrity() error {
	var result string
	if err := s.DB.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("数据库完整性检查失败")
	}
	rows, err := s.DB.Query("SELECT password FROM credentials UNION ALL SELECT agent_secret FROM nodes UNION ALL SELECT token_secret FROM devices")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return err
		}
		if _, err = s.Vault.Decrypt(v); err != nil {
			return secretError(err)
		}
	}
	return rows.Err()
}

var _ = sql.ErrNoRows

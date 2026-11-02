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
	var exists int
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE lower(json_extract(config,'$.domain'))=lower(?) OR (json_extract(config,'$.sshHost')=? AND json_extract(config,'$.port')=?)", n.Domain, n.SSHHost, n.Port).Scan(&exists); err != nil {
		return n, err
	}
	if exists > 0 {
		return n, errors.New("该域名或服务器端口已添加，请在原节点重试")
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
	var raw, metrics, online string
	var last int64
	var desired, applied int64
	var healthy bool
	err := s.DB.QueryRow("SELECT config,desired_revision,applied_revision,last_seen,metrics,online,core_healthy FROM nodes WHERE id=?", id).Scan(&raw, &desired, &applied, &last, &metrics, &online, &healthy)
	if err != nil {
		return n, notFound(err)
	}
	if err = json.Unmarshal([]byte(raw), &n); err != nil {
		return n, err
	}
	_ = json.Unmarshal([]byte(metrics), &n.Metrics)
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
	rows, err := s.DB.Query("SELECT id FROM nodes ORDER BY json_extract(config,'$.createdAt') DESC")
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
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.name',?,'$.bandwidthMbps',?,'$.monthlyQuotaBytes',?) WHERE id=?", n.Name, n.BandwidthMbps, n.MonthlyQuotaBytes, n.ID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) SetStatus(id, status, message string, shared bool) error {
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.status',?,'$.message',?,'$.sharedHost',json(?)) WHERE id=?", status, message, jsonString(shared), id)
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
	_, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.controlOnNode',json(?),'$.acmeViaCaddy',json(?),'$.sharedHost',json(?)) WHERE id=?", jsonString(controlOnNode), jsonString(acmeViaCaddy), jsonString(acmeViaCaddy), id)
	return err
}
func (s *Store) Pin(id, fingerprint string) error {
	if fingerprint == "" {
		return errors.New("SSH 主机指纹不能为空")
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
	result, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.status','ready','$.message','外部 Mihomo 验证通过') WHERE id=? AND json_extract(config,'$.status') IN ('awaiting_external','ready')", id)
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
	result.Devices = []model.AgentDevice{}
	err := s.DB.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", id).Scan(&result.Revision)
	if err != nil {
		return result, notFound(err)
	}
	rows, err := s.DB.Query(`SELECT c.device_id,c.password,
CASE WHEN d.id IS NULL THEN 1 WHEN d.enabled=0 THEN 0 WHEN a.node_id IS NOT NULL THEN 1 WHEN r.id IS NOT NULL AND r.immediate=0 AND r.status!='revoked' THEN 1 ELSE 0 END,
COALESCE(r.id,''),CASE WHEN r.id IS NOT NULL AND r.immediate=0 AND r.status!='revoked' THEN 60 ELSE 0 END
FROM credentials c LEFT JOIN devices d ON d.id=c.device_id
LEFT JOIN assignments a ON a.device_id=c.device_id AND a.node_id=c.node_id
LEFT JOIN removals r ON r.id=(SELECT rr.id FROM removals rr WHERE rr.device_id=c.device_id AND rr.node_id=c.node_id AND rr.status!='cancelled' ORDER BY rr.created_at DESC,rr.rowid DESC LIMIT 1) AND a.node_id IS NULL
WHERE c.node_id=? ORDER BY c.device_id`, id)
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
	}
	return result, rows.Err()
}

type Job struct {
	ID           int64
	NodeID, Kind string
}

func (s *Store) RecoverJobs() error {
	_, err := s.DB.Exec("UPDATE jobs SET state='pending' WHERE state='running'")
	return err
}
func (s *Store) NextJob() (Job, error) {
	var j Job
	tx, err := s.DB.Begin()
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	err = tx.QueryRow("SELECT id,node_id,kind FROM jobs WHERE state='pending' ORDER BY id LIMIT 1").Scan(&j.ID, &j.NodeID, &j.Kind)
	if err != nil {
		return j, notFound(err)
	}
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
	_, _ = s.DB.Exec("UPDATE jobs SET state=? WHERE id=?", state, j.ID)
}
func (s *Store) Queue(id, kind, password string) (model.Node, error) {
	n, err := s.Node(id)
	if err != nil {
		return n, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return n, err
	}
	defer tx.Rollback()
	if kind == "install" {
		if password == "" {
			return n, errors.New("重试部署需要重新输入 SSH 密码")
		}
		enc, e := s.Vault.Encrypt(password)
		if e != nil {
			return n, e
		}
		if _, err = tx.Exec("UPDATE nodes SET password=?,password_expires=? WHERE id=?", enc, s.Now().Add(24*time.Hour).Unix(), id); err != nil {
			return n, err
		}
		n.Status = "pending"
		n.Message = "等待重新部署"
	} else {
		n.Status = "verifying"
		n.Message = "等待连通性验证"
	}
	if _, err = tx.Exec("INSERT INTO jobs(node_id,kind,created_at) VALUES(?,?,?)", id, kind, s.Now().Unix()); err != nil {
		return n, errors.New("节点已有部署或验证任务，请等待当前任务完成")
	}
	if _, err = tx.Exec("UPDATE nodes SET config=json_set(config,'$.status',?,'$.message',?) WHERE id=?", n.Status, n.Message, id); err != nil {
		return n, err
	}
	if err = tx.Commit(); err != nil {
		return n, err
	}
	s.Audit("node."+kind, id)
	return n, nil
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

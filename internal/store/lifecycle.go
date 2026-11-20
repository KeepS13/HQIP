package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func (s *Store) DeleteDevice(id string) (model.Device, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Device{}, err
	}
	defer tx.Rollback()
	var deleted int64
	if err = tx.QueryRow("SELECT deleted_at FROM devices WHERE id=?", id).Scan(&deleted); err != nil {
		return model.Device{}, notFound(err)
	}
	if deleted > 0 {
		tx.Rollback()
		return s.Device(id)
	}
	if _, err = tx.Exec("UPDATE devices SET enabled=0,deleted_at=?,token_hash=? WHERE id=?", s.Now().Unix(), secure.Hash(secure.Token()), id); err != nil {
		return model.Device{}, err
	}
	if _, err = tx.Exec("UPDATE client_sessions SET revoked=1 WHERE device_id=?", id); err != nil {
		return model.Device{}, err
	}
	if _, err = tx.Exec("DELETE FROM device_accounts WHERE device_id=?", id); err != nil {
		return model.Device{}, err
	}
	if _, err = tx.Exec("UPDATE device_config SET revision=revision+1,default_node_id='' WHERE device_id=?", id); err != nil {
		return model.Device{}, err
	}
	// Retain credentials as accounting keys; the deleted device is never enabled.
	rows, err := tx.Query("SELECT node_id FROM credentials WHERE device_id=?", id)
	if err != nil {
		return model.Device{}, err
	}
	ids := []string{}
	for rows.Next() {
		var nid string
		if err = rows.Scan(&nid); err != nil {
			rows.Close()
			return model.Device{}, err
		}
		ids = append(ids, nid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return model.Device{}, err
	}
	for _, nid := range ids {
		if _, err = tx.Exec("UPDATE removals SET status='cancelled',immediate=1 WHERE device_id=? AND node_id=? AND status NOT IN ('cancelled','revoked')", id, nid); err != nil {
			return model.Device{}, err
		}
		if _, err = tx.Exec(`INSERT INTO removals(id,device_id,node_id,revision,created_at,immediate,status)
SELECT ?,?,?,dc.revision,?,1,CASE WHEN n.lifecycle='deleted' OR (n.managed=0 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.node_id=n.id AND j.state IN ('running','cancelling'))) THEN 'revoked' ELSE 'pending' END
FROM device_config dc JOIN nodes n ON n.id=? WHERE dc.device_id=?`, NewID(), id, nid, s.Now().Unix(), nid, id); err != nil {
			return model.Device{}, err
		}
		if _, err = tx.Exec("UPDATE nodes SET desired_revision=desired_revision+1 WHERE id=?", nid); err != nil {
			return model.Device{}, err
		}
	}
	if _, err = tx.Exec("DELETE FROM assignments WHERE device_id=?", id); err != nil {
		return model.Device{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Device{}, err
	}
	s.Audit("device.delete", id)
	return s.Device(id)
}

func (s *Store) DeleteNode(id string, options model.NodeDeleteOptions) (model.Node, error) {
	return retryDatabase(func() (model.Node, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return model.Node{}, err
		}
		defer tx.Rollback()
		var revision int64
		var lifecycle string
		var managed bool
		if err = tx.QueryRow("SELECT revision,lifecycle,managed FROM nodes WHERE id=?", id).Scan(&revision, &lifecycle, &managed); err != nil {
			return model.Node{}, notFound(err)
		}
		references, err := nodeSubscriptionReferences(tx, id)
		if err != nil {
			return model.Node{}, err
		}
		userReferences, err := nodeUserSubscriptionReferences(tx, id, s.Now())
		if err != nil {
			return model.Node{}, err
		}
		if len(references) > 0 || len(userReferences) > 0 {
			return model.Node{}, &NodeSubscriptionConflict{Templates: references, UserSubscriptions: userReferences}
		}
		if lifecycle != "active" {
			tx.Rollback()
			return s.Node(id)
		}
		if options.ExpectedRevision != revision {
			return model.Node{}, ErrConflict
		}
		type impacted struct {
			id, defaultID string
			revision      int64
		}
		rows, err := tx.Query("SELECT dc.device_id,dc.default_node_id,dc.revision FROM device_config dc JOIN assignments a ON a.device_id=dc.device_id WHERE a.node_id=?", id)
		if err != nil {
			return model.Node{}, err
		}
		devices := []impacted{}
		for rows.Next() {
			var d impacted
			if err = rows.Scan(&d.id, &d.defaultID, &d.revision); err != nil {
				rows.Close()
				return model.Node{}, err
			}
			devices = append(devices, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return model.Node{}, err
		}
		for _, d := range devices {
			replacement, provided := options.Replacements[d.id]
			if !provided || replacement.ExpectedRevision != d.revision {
				return model.Node{}, ErrConflict
			}
			newDefault := d.defaultID
			if d.defaultID == id {
				var remaining int
				if err = tx.QueryRow("SELECT COUNT(*) FROM assignments a JOIN nodes n ON n.id=a.node_id WHERE a.device_id=? AND a.node_id!=? AND n.lifecycle='active'", d.id, id).Scan(&remaining); err != nil {
					return model.Node{}, err
				}
				if remaining == 0 {
					newDefault = ""
				} else {
					r := replacement
					var valid int
					if err = tx.QueryRow("SELECT COUNT(*) FROM assignments a JOIN nodes n ON n.id=a.node_id WHERE a.device_id=? AND a.node_id=? AND a.node_id!=? AND n.lifecycle='active'", d.id, r.DefaultNodeID, id).Scan(&valid); err != nil {
						return model.Node{}, err
					}
					if valid != 1 {
						return model.Node{}, errors.New("替代默认节点必须属于设备剩余授权节点")
					}
					newDefault = r.DefaultNodeID
				}
			}
			if _, err = tx.Exec("DELETE FROM assignments WHERE device_id=? AND node_id=?", d.id, id); err != nil {
				return model.Node{}, err
			}
			if _, err = tx.Exec("UPDATE device_config SET revision=revision+1,default_node_id=? WHERE device_id=?", newDefault, d.id); err != nil {
				return model.Node{}, err
			}
			if _, err = tx.Exec("UPDATE removals SET status='cancelled',immediate=1 WHERE node_id=? AND device_id=? AND status NOT IN ('revoked','cancelled')", id, d.id); err != nil {
				return model.Node{}, err
			}
			if _, err = tx.Exec("INSERT INTO removals(id,device_id,node_id,revision,created_at,immediate) VALUES(?,?,?,?,?,1)", NewID(), d.id, id, d.revision+1, s.Now().Unix()); err != nil {
				return model.Node{}, err
			}
		}
		var running int
		if err = tx.QueryRow("SELECT COUNT(*) FROM jobs WHERE node_id=? AND state='running'", id).Scan(&running); err != nil {
			return model.Node{}, err
		}
		// A running installer may already have written an owner. Retain the
		// tombstone in 'retiring' until its cancellation and ownership are settled.
		next := "retiring"
		message := "等待节点停止并清理"
		if !managed && running == 0 {
			next = "deleted"
			message = "未安装节点记录已删除"
		}
		if _, err = tx.Exec("UPDATE jobs SET state=CASE WHEN state='running' THEN 'cancelling' ELSE 'cancelled' END WHERE node_id=? AND state IN ('pending','running')", id); err != nil {
			return model.Node{}, err
		}
		if _, err = tx.Exec("UPDATE nodes SET password='',password_expires=0 WHERE id=? AND EXISTS(SELECT 1 FROM node_network_quality q JOIN jobs j ON j.id=q.job_id WHERE q.node_id=? AND j.kind='quality' AND q.status IN ('queued','running'))", id, id); err != nil {
			return model.Node{}, err
		}
		if _, err = tx.Exec("UPDATE node_network_quality SET status='cancelled',finished_at=?,error='节点已删除，检测已取消' WHERE node_id=? AND status IN ('queued','running')", s.Now().Unix(), id); err != nil {
			return model.Node{}, err
		}
		if _, err = tx.Exec("UPDATE nodes SET lifecycle=?,deleted_at=?,revision=revision+1,desired_revision=desired_revision+1,config=json_set(config,'$.status',?,'$.message',?) WHERE id=?", next, s.Now().Unix(), next, message, id); err != nil {
			return model.Node{}, err
		}
		if next == "retiring" {
			if _, err = tx.Exec("INSERT INTO node_retirements(node_id,id,created_at) VALUES(?,?,?)", id, NewID(), s.Now().Unix()); err != nil {
				return model.Node{}, err
			}
		} else {
			if _, err = tx.Exec("UPDATE nodes SET password='',password_expires=0 WHERE id=?", id); err != nil {
				return model.Node{}, err
			}
			if _, err = tx.Exec("UPDATE removals SET status='revoked',immediate=1 WHERE node_id=? AND status!='cancelled'", id); err != nil {
				return model.Node{}, err
			}
		}
		if err = tx.Commit(); err != nil {
			return model.Node{}, err
		}
		s.Audit("node.delete", id)
		return s.Node(id)
	})
}

// MarkInstalled must be called whenever an installer has created our owner,
// even if its subsequent certificate or service validation fails.
func (s *Store) MarkInstalled(id string) error {
	_, err := s.DB.Exec("UPDATE nodes SET managed=1 WHERE id=? AND lifecycle!='deleted'", id)
	return err
}
func (s *Store) SetInstallationVersion(id string, version int) error {
	if version < 1 || version > 2 {
		return errors.New("安装版本无效")
	}
	res, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.installationVersion',?) WHERE id=? AND lifecycle='active'", version, id)
	if err != nil {
		return err
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) CompleteNodeRetirement(id string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE node_retirements SET stopped=1,final=1 WHERE node_id=?", id); err != nil {
		return err
	}
	res, err := tx.Exec("UPDATE nodes SET lifecycle='deleted',password='',password_expires=0,core_healthy=0,online='{}',config=json_set(config,'$.status','deleted','$.message','节点服务已停止并清理') WHERE id=? AND lifecycle IN ('retiring','deleted')", id)
	if err != nil {
		return err
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec("UPDATE removals SET status='revoked',immediate=1 WHERE node_id=? AND status!='cancelled'", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RetirementNodes() ([]model.Node, error) {
	rows, err := s.DB.Query("SELECT id FROM nodes WHERE lifecycle='retiring'")
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
	result := []model.Node{}
	for _, id := range ids {
		n, e := s.Node(id)
		if e != nil {
			return nil, e
		}
		result = append(result, n)
	}
	return result, nil
}

func (s *Store) DeletedNodes() ([]model.Node, error) {
	rows, err := s.DB.Query("SELECT id FROM nodes WHERE lifecycle='deleted' ORDER BY deleted_at,id")
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
	result := []model.Node{}
	for _, id := range ids {
		n, e := s.Node(id)
		if e != nil {
			return nil, e
		}
		result = append(result, n)
	}
	return result, nil
}
func (s *Store) SavePreflight(id, fingerprint string, takeover bool) error {
	if fingerprint == "" || len(fingerprint) > 256 {
		return errors.New("预检指纹无效")
	}
	_, err := s.DB.Exec("INSERT INTO node_preflights(node_id,fingerprint,takeover,created_at) SELECT id,?,?,? FROM nodes WHERE id=? AND lifecycle='active' ON CONFLICT(node_id) DO UPDATE SET fingerprint=excluded.fingerprint,takeover=excluded.takeover,created_at=excluded.created_at", fingerprint, takeover, s.Now().Unix(), id)
	return err
}
func (s *Store) QueueDeployment(id, password string, takeover bool, preflightFingerprint string) (model.Node, error) {
	return s.queueWithOptions(id, "install", password, Job{Takeover: takeover, PreflightFingerprint: preflightFingerprint})
}

// QueueDeploymentCertificate stores the requested mode on the durable job.
// The live mode changes only after successful installation, so rollback cannot
// leave the controller claiming that the node runs another certificate mode.
func (s *Store) QueueDeploymentCertificate(id, password string, takeover bool, preflightFingerprint, certificateMode, zoneID string) (model.Node, error) {
	if certificateMode != "http" && certificateMode != "cloudflare" {
		return model.Node{}, errors.New("证书模式无效")
	}
	if certificateMode == "cloudflare" && (len(zoneID) != 32 || strings.Trim(zoneID, "0123456789abcdefABCDEF") != "") {
		return model.Node{}, errors.New("请选择有效的 Cloudflare Zone")
	}
	return s.queueWithOptions(id, "install", password, Job{Takeover: takeover, PreflightFingerprint: preflightFingerprint, CertificateMode: certificateMode, ZoneID: zoneID})
}
func (s *Store) QueueRetirement(id, password string) (model.Node, error) {
	return s.queueWithOptions(id, "retire", password, Job{})
}
func (s *Store) JobActive(id int64) (bool, error) {
	var state string
	err := s.DB.QueryRow("SELECT state FROM jobs WHERE id=?", id).Scan(&state)
	return state == "running", notFound(err)
}
func (s *Store) queueWithOptions(id, kind, password string, options Job) (model.Node, error) {
	n, err := s.Node(id)
	if err != nil {
		return n, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return n, err
	}
	defer tx.Rollback()
	var lifecycle, currentConfig string
	if err = tx.QueryRow("SELECT lifecycle,config FROM nodes WHERE id=?", id).Scan(&lifecycle, &currentConfig); err != nil {
		return n, notFound(err)
	}
	var current model.Node
	if err = json.Unmarshal([]byte(currentConfig), &current); err != nil {
		return n, err
	}
	if current.IPSwitch != nil && (kind == "verify" || options.Takeover || options.CertificateMode != "") {
		return n, errors.New("公网 IP 切换尚未完成，请先重试部署或删除此节点")
	}
	if kind == "retire" {
		if lifecycle != "retiring" {
			return n, ErrConflict
		}
	} else if lifecycle != "active" {
		return n, ErrConflict
	}
	if options.Takeover {
		var hash string
		var allowed bool
		var at int64
		if err = tx.QueryRow("SELECT fingerprint,takeover,created_at FROM node_preflights WHERE node_id=?", id).Scan(&hash, &allowed, &at); err != nil || !allowed || hash != options.PreflightFingerprint || at < s.Now().Add(-15*time.Minute).Unix() {
			return n, errors.New("接管预检已过期或发生变化，请重新预检")
		}
	}
	if options.CertificateMode != "" {
		if n.DNSMode == "cloudflare" && options.CertificateMode != "cloudflare" {
			return n, errors.New("自动分配域名需要 Cloudflare 证书模式")
		}
	}
	if kind == "install" || kind == "retire" {
		if password == "" {
			return n, errors.New("此操作需要重新输入 SSH 密码")
		}
		enc, e := s.Vault.Encrypt(password)
		if e != nil {
			return n, e
		}
		if _, err = tx.Exec("UPDATE nodes SET password=?,password_expires=? WHERE id=?", enc, s.Now().Add(24*time.Hour).Unix(), id); err != nil {
			return n, err
		}
	}
	// Cancelling jobs retain their server lock until their worker exits.
	var cancelling int
	if err = tx.QueryRow("SELECT COUNT(*) FROM jobs WHERE node_id=? AND state='cancelling'", id).Scan(&cancelling); err != nil {
		return n, err
	}
	if cancelling > 0 {
		return n, errors.New("旧部署任务正在停止，请稍后重试")
	}
	var queuedJobID int64
	if err = tx.QueryRow("INSERT INTO jobs(node_id,kind,created_at,options) VALUES(?,?,?,?) RETURNING id", id, kind, s.Now().Unix(), jsonString(options)).Scan(&queuedJobID); err != nil {
		return n, errors.New("节点已有任务，请等待当前任务完成")
	}
	if kind != "retire" {
		status, message := "verifying", "等待连通性验证"
		if kind == "install" {
			status, message = "pending", "等待重新部署"
		}
		if _, err = tx.Exec("UPDATE nodes SET config=json_set(config,'$.status',?,'$.message',?) WHERE id=?", status, message, id); err != nil {
			return n, err
		}
	}
	if err = tx.Commit(); err != nil {
		return n, err
	}
	s.Audit("node."+kind, id)
	n, err = s.Node(id)
	n.QueuedJobID = queuedJobID
	return n, err
}

func (s *Store) CommitNodeCertificateMode(id string, jobID int64, mode, zoneID string) error {
	if mode != "http" && mode != "cloudflare" {
		return errors.New("证书模式无效")
	}
	res, err := s.DB.Exec(`UPDATE nodes SET revision=revision+1,desired_revision=desired_revision+1,certificate_status='{}',config=json_set(config,'$.certificateMode',?,'$.zoneId',?)
WHERE id=? AND lifecycle='active' AND EXISTS(SELECT 1 FROM jobs WHERE id=? AND node_id=nodes.id AND state='running' AND json_extract(options,'$.certificateMode')=? AND COALESCE(json_extract(options,'$.zoneId'),'')=?)`, mode, zoneID, id, jobID, mode, zoneID)
	if err != nil {
		return err
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) validateCertificateReport(nodeID string, status *model.CertificateStatus) error {
	if status == nil {
		return nil
	}
	if status.Version < 0 || len(status.Error) > 500 {
		return errors.New("证书状态无效")
	}
	if status.Version == 0 {
		// A candidate installation can briefly report its old HTTP telemetry
		// before the worker commits its new certificate mode. It does not claim
		// application of any issued certificate version.
		return nil
	}
	bundle, err := s.CertificateBundle(nodeID)
	if err != nil {
		return err
	}
	if bundle == nil || status.Version > bundle.Version {
		return errors.New("节点回报的证书版本无效")
	}
	if status.Version == bundle.Version && status.Error == "" && !status.NotAfter.Equal(bundle.NotAfter) {
		return errors.New("节点回报的证书有效期不匹配")
	}
	return nil
}

func (s *Store) recordLifecycle(tx *sql.Tx, nodeID string, r model.AgentReport) error {
	if len(r.Capabilities) > 32 {
		return errors.New("节点能力列表过长")
	}
	for _, c := range r.Capabilities {
		if len(c) > 80 {
			return errors.New("节点能力名称过长")
		}
	}
	if r.Capabilities != nil {
		if _, err := tx.Exec("UPDATE nodes SET capabilities=? WHERE id=?", jsonString(r.Capabilities), nodeID); err != nil {
			return err
		}
	}
	if r.Certificate != nil {
		if r.Certificate.Version < 0 || len(r.Certificate.Error) > 500 {
			return errors.New("证书状态无效")
		}
		status := *r.Certificate
		if status.Error != "" {
			status.Error = "节点证书应用失败，请检查节点日志"
		}
		if _, err := tx.Exec("UPDATE nodes SET certificate_status=? WHERE id=? AND COALESCE(json_extract(certificate_status,'$.version'),0)<=?", jsonString(status), nodeID, status.Version); err != nil {
			return err
		}
	}
	if r.Retirement == nil {
		return nil
	}
	var id string
	var stopped bool
	err := tx.QueryRow("SELECT id,stopped FROM node_retirements WHERE node_id=?", nodeID).Scan(&id, &stopped)
	if err != nil {
		return notFound(err)
	}
	if id != r.Retirement.ID || r.Retirement.Final && (!stopped || !r.Retirement.Stopped) {
		return errors.New("节点停止回执无效")
	}
	if _, err = tx.Exec("UPDATE node_retirements SET stopped=MAX(stopped,?),final=MAX(final,?) WHERE node_id=?", r.Retirement.Stopped, r.Retirement.Final, nodeID); err != nil {
		return err
	}
	if r.Retirement.Stopped {
		if _, err = tx.Exec("UPDATE nodes SET core_healthy=0,online='{}' WHERE id=?", nodeID); err != nil {
			return err
		}
	}
	if r.Retirement.Final {
		if _, err = tx.Exec("UPDATE nodes SET lifecycle='deleted',password='',password_expires=0,config=json_set(config,'$.status','deleted','$.message','节点服务已停止并清理') WHERE id=? AND lifecycle='retiring'", nodeID); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE removals SET status='revoked',immediate=1 WHERE node_id=? AND status!='cancelled'", nodeID); err != nil {
			return err
		}
	}
	return nil
}

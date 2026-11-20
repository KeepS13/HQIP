package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/cloudflare"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/netutil"
)

// QueueNodeIPSwitch keeps node identity, credentials and accounting intact.
// Both addresses remain reserved until installation succeeds (or retirement).
func (s *Store) QueueNodeIPSwitch(id, fromIP, toIP, password string, nodeRevision, serverRevision int64, manualDNSConfirmed bool) (model.Node, error) {
	var n model.Node
	ip, err := netutil.ParsePublicIPv4(toIP)
	if err != nil {
		return n, err
	}
	if password == "" || len(password) > 1024 {
		return n, errors.New("请填写 SSH 密码或使用已保存的凭据")
	}
	current, err := s.Node(id)
	if err != nil {
		return n, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return n, err
	}
	defer tx.Rollback()
	// Lock the server against concurrent connection edits before reading it.
	var raw string
	err = tx.QueryRow("UPDATE servers SET revision=revision WHERE id=? AND revision=? AND deleted_at=0 RETURNING config", current.ServerID, serverRevision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrConflict
	}
	if err != nil {
		return n, err
	}
	var server model.Server
	if err = json.Unmarshal([]byte(raw), &server); err != nil {
		return n, err
	}
	if server.SSHHost != ip || server.HostKey == "" || server.MachineID == "" {
		return n, errors.New("请先修改 SSH 公网 IPv4 并通过主机身份检查")
	}
	if err = ensureServerIdle(tx, server.ID); err != nil {
		return n, err
	}
	err = tx.QueryRow("UPDATE nodes SET revision=revision WHERE id=? AND revision=? AND lifecycle='active' AND managed=1 RETURNING config", id, nodeRevision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrConflict
	}
	if err != nil {
		return n, err
	}
	if err = json.Unmarshal([]byte(raw), &n); err != nil {
		return n, err
	}
	if n.ServerID != server.ID || n.PublicIP != fromIP || fromIP == ip || n.IPSwitch != nil || n.InstallationVersion != 2 {
		return n, errors.New("节点公网 IP 或部署状态已变化，请刷新后重试")
	}
	if n.DNSMode != "cloudflare" && !manualDNSConfirmed {
		return n, errors.New("请先将节点域名的 A 记录改为新 IP，并确认已完成解析修改")
	}
	var busy int
	if err = tx.QueryRow("SELECT COUNT(*) FROM jobs WHERE node_id=? AND state IN ('pending','running','cancelling')", id).Scan(&busy); err != nil {
		return n, err
	}
	if busy > 0 {
		return n, errors.New("节点已有任务，请等待当前任务完成")
	}
	var addressID string
	err = tx.QueryRow("SELECT id FROM server_addresses WHERE server_id=? AND ip=? AND deleted_at=0", server.ID, ip).Scan(&addressID)
	if errors.Is(err, sql.ErrNoRows) {
		addressID = NewID()
		if _, err = tx.Exec("INSERT INTO server_addresses(id,server_id,ip,created_at) VALUES(?,?,?,?)", addressID, server.ID, ip, s.Now().Unix()); err != nil {
			return n, err
		}
	} else if err != nil {
		return n, err
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE id!=? AND lifecycle!='deleted' AND (json_extract(config,'$.addressId')=? OR json_extract(json_extract(config,'$.ipSwitch'),'$.fromAddressId')=?)", id, addressID, addressID).Scan(&busy); err != nil {
		return n, err
	}
	if busy > 0 {
		return n, ErrAddressInUse
	}
	n.IPSwitch = &model.NodeIPSwitch{FromIP: fromIP, FromAddressID: n.AddressID}
	n.PublicIP, n.AddressID = ip, addressID
	n.Status, n.Message = "pending", "等待切换公网 IP；完成部署验证前不可用"
	enc, err := s.Vault.Encrypt(password)
	if err != nil {
		return n, err
	}
	if _, err = tx.Exec("UPDATE nodes SET config=?,password=?,password_expires=?,revision=revision+1,desired_revision=desired_revision+1,core_healthy=0 WHERE id=?", jsonString(n), enc, s.Now().Add(24*time.Hour).Unix(), id); err != nil {
		return n, err
	}
	var jobID int64
	if err = tx.QueryRow("INSERT INTO jobs(node_id,kind,created_at) VALUES(?,'install',?) RETURNING id", id, s.Now().Unix()).Scan(&jobID); err != nil {
		return n, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'node.public_ip.switch',?)", s.Now().Unix(), id); err != nil {
		return n, err
	}
	if err = tx.Commit(); err != nil {
		return n, err
	}
	n, err = s.Node(id)
	n.QueuedJobID = jobID
	return n, err
}

func (s *Store) CompleteNodeIPSwitch(id string, jobID int64, ip string) error {
	res, err := s.DB.Exec(`UPDATE nodes SET config=json_set(config,'$.ipSwitch',NULL),revision=revision+1
WHERE id=? AND lifecycle='active' AND json_extract(config,'$.publicIp')=?
AND EXISTS(SELECT 1 FROM jobs WHERE id=? AND node_id=nodes.id AND kind='install' AND state='running')`, id, ip, jobID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// CompleteIPSwitchDNS is called after the owned record was updated remotely.
// A lost response can be retried using the still-persisted old record.
func (s *Store) CompleteIPSwitchDNS(intent DNSIntent, record cloudflare.Record) error {
	before := intent.Record
	want := before
	want.Content = record.Content
	if before.ID == "" || before.ID != record.ID || before.Type != "A" || !cloudflare.SameRecord(want, record) {
		return cloudflare.ErrChanged
	}
	res, err := s.DB.Exec(`UPDATE cloudflare_dns SET record=?,state='active',error=''
WHERE id=? AND record=? AND state IN ('pending','active','error')
AND EXISTS(SELECT 1 FROM nodes WHERE id=cloudflare_dns.node_id AND lifecycle='active'
AND json_extract(json_extract(config,'$.ipSwitch'),'$.fromIp')=? AND json_extract(config,'$.publicIp')=?)`, jsonString(record), intent.ID, jsonString(before), before.Content, record.Content)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

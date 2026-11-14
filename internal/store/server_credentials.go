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

type SSHCredentialStatus struct {
	ServerID    string     `json:"serverId"`
	Configured  bool       `json:"configured"`
	Revision    int64      `json:"revision"`
	Status      string     `json:"status"`
	Message     string     `json:"message"`
	UpdatedAt   *time.Time `json:"updatedAt"`
	LastChecked *time.Time `json:"lastChecked"`
	NextCheck   *time.Time `json:"nextCheck"`
}

func serverBinding(v model.Server) string {
	return secure.Hash(jsonString([]any{v.SSHHost, v.SSHPort, v.SSHUser, v.HostKey, v.MachineID}))
}
func validSSHPassword(password string) error {
	if password == "" || len(password) > 1024 || strings.ContainsAny(password, "\r\n\x00") {
		return errors.New("SSH 密码长度应为 1～1024 字节，不能包含换行或空字符")
	}
	return nil
}
func serverInTransaction(tx *sql.Tx, id string) (model.Server, error) {
	var v model.Server
	var raw string
	var revision int64
	err := tx.QueryRow("SELECT config,revision FROM servers WHERE id=? AND deleted_at=0", id).Scan(&raw, &revision)
	if err != nil {
		return v, notFound(err)
	}
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return v, err
	}
	v.Revision = revision
	return v, nil
}
func ensureServerIdle(tx *sql.Tx, id string) error {
	var count int
	err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM server_operations WHERE server_id=? AND state IN ('pending','running','uncertain'))+(SELECT COUNT(*) FROM jobs j JOIN nodes n ON n.id=j.node_id WHERE json_extract(n.config,'$.serverId')=? AND j.state IN ('running','cancelling'))`, id, id).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return errors.New("服务器有待完成的运维任务，请完成后再修改凭据或连接")
	}
	return nil
}
func (s *Store) saveServerCredential(tx *sql.Tx, v model.Server, password string) error {
	if err := validSSHPassword(password); err != nil {
		return err
	}
	if v.HostKey == "" || v.MachineID == "" {
		return errors.New("请先验证 SSH 主机身份")
	}
	encrypted, err := s.Vault.Encrypt(password)
	if err != nil {
		return err
	}
	now := s.Now().Unix()
	_, err = tx.Exec(`INSERT INTO server_credentials(server_id,secret,binding,updated_at,last_checked,next_check,status) VALUES(?,?,?,?,?,?,'healthy')
ON CONFLICT(server_id) DO UPDATE SET secret=excluded.secret,binding=excluded.binding,revision=server_credentials.revision+1,status='healthy',message='',updated_at=excluded.updated_at,last_checked=excluded.last_checked,next_check=excluded.next_check`, v.ID, encrypted, serverBinding(v), now, now, now+3600)
	return err
}
func (s *Store) ServerCredential(id string) (SSHCredentialStatus, error) {
	v, err := s.Server(id)
	if err != nil {
		return SSHCredentialStatus{}, err
	}
	out := SSHCredentialStatus{ServerID: id, Status: "not-configured"}
	var binding string
	var updated, checked, next int64
	err = s.DB.QueryRow("SELECT revision,status,message,updated_at,last_checked,next_check,binding FROM server_credentials WHERE server_id=?", id).Scan(&out.Revision, &out.Status, &out.Message, &updated, &checked, &next, &binding)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Configured = true
	out.UpdatedAt = alertTime(updated)
	out.LastChecked = alertTime(checked)
	out.NextCheck = alertTime(next)
	if binding != serverBinding(v) {
		out.Status = "identity-changed"
		out.Message = "SSH 连接信息已改变，请重新保存并验证凭据"
	}
	return out, nil
}
func (s *Store) SaveServerCredential(id, password string, serverRevision, credentialRevision int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := serverInTransaction(tx, id)
	if err != nil {
		return err
	}
	if v.Revision != serverRevision {
		return ErrConflict
	}
	if err = ensureServerIdle(tx, id); err != nil {
		return err
	}
	var revision int64
	err = tx.QueryRow("SELECT revision FROM server_credentials WHERE server_id=?", id).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if revision != credentialRevision {
		return ErrConflict
	}
	if err = s.saveServerCredential(tx, v, password); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'server.credential.save',?)", s.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DeleteServerCredential(id string, revision int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = serverInTransaction(tx, id); err != nil {
		return err
	}
	if err = ensureServerIdle(tx, id); err != nil {
		return err
	}
	result, err := tx.Exec("DELETE FROM server_credentials WHERE server_id=? AND revision=?", id, revision)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'server.credential.delete',?)", s.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ServerPassword(v model.Server) (string, error) {
	var secret, binding, status string
	err := s.DB.QueryRow("SELECT secret,binding,status FROM server_credentials WHERE server_id=?", v.ID).Scan(&secret, &binding, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("请先为此服务器保存 SSH 凭据")
	}
	if err != nil {
		return "", err
	}
	if binding != serverBinding(v) {
		return "", errors.New("SSH 连接信息已改变，请重新验证凭据")
	}
	var uncertain int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM server_operations WHERE server_id=? AND kind='password' AND state IN ('pending','running','uncertain')", v.ID).Scan(&uncertain); err != nil {
		return "", err
	}
	if uncertain > 0 {
		return "", errors.New("服务器改密结果尚待确认，请先完成改密任务")
	}
	value, err := s.Vault.Decrypt(secret)
	return value, secretError(err)
}

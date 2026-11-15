package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type ServerOperation struct {
	ID                 string `json:"id"`
	ServerID           string `json:"serverId"`
	NodeID             string `json:"nodeId"`
	ServerName         string `json:"serverName,omitempty"`
	NodeName           string `json:"nodeName,omitempty"`
	Kind               string `json:"kind"`
	State              string `json:"state"`
	CredentialRevision int64  `json:"credentialRevision"`
	Binding            string `json:"-"`
	Secret             string `json:"-"`
	Target             string `json:"target"`
	Message            string `json:"message"`
	CreatedAt          int64  `json:"createdAt"`
	StartedAt          int64  `json:"startedAt"`
	FinishedAt         int64  `json:"finishedAt"`
}

const operationColumns = "id,server_id,node_id,kind,state,credential_revision,binding,secret,target,message,created_at,started_at,finished_at"

func scanOperation(row interface{ Scan(...any) error }) (ServerOperation, error) {
	var j ServerOperation
	err := row.Scan(&j.ID, &j.ServerID, &j.NodeID, &j.Kind, &j.State, &j.CredentialRevision, &j.Binding, &j.Secret, &j.Target, &j.Message, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	return j, notFound(err)
}
func (s *Store) ServerOperations(serverID string) ([]ServerOperation, error) {
	query := `SELECT o.id,o.server_id,o.node_id,o.kind,o.state,o.credential_revision,o.binding,o.secret,o.target,o.message,o.created_at,o.started_at,o.finished_at,
COALESCE(json_extract(s.config,'$.name'),''),COALESCE(json_extract(n.config,'$.name'),'')
FROM server_operations o LEFT JOIN servers s ON s.id=o.server_id LEFT JOIN nodes n ON n.id=o.node_id`
	var args []any
	if serverID != "" {
		query += " WHERE o.server_id=?"
		args = append(args, serverID)
	}
	rows, err := s.DB.Query(query+" ORDER BY o.created_at DESC,o.id DESC LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServerOperation{}
	for rows.Next() {
		var j ServerOperation
		e := rows.Scan(&j.ID, &j.ServerID, &j.NodeID, &j.Kind, &j.State, &j.CredentialRevision, &j.Binding, &j.Secret, &j.Target, &j.Message, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.ServerName, &j.NodeName)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) QueueServerOperation(id, node, kind, password, target string, revision int64) (ServerOperation, error) {
	j := ServerOperation{ID: NewID(), ServerID: id, NodeID: node, Kind: kind, State: "pending", Target: target, CreatedAt: s.Now().Unix()}
	if kind != "check" && kind != "agent-upgrade" {
		return j, errors.New("远程改密已停用；仅支持 SSH 巡检和 Agent 升级")
	}
	if password != "" {
		return j, errors.New("运维任务不能设置服务器密码")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	v, err := serverInTransaction(tx, id)
	if err != nil {
		return j, err
	}
	if revision != 0 && v.Revision != revision {
		return j, ErrConflict
	}
	err = tx.QueryRow("SELECT revision,binding FROM server_credentials WHERE server_id=?", id).Scan(&j.CredentialRevision, &j.Binding)
	if errors.Is(err, sql.ErrNoRows) {
		return j, errors.New("请先保存此服务器的 SSH 凭据")
	}
	if err != nil {
		return j, err
	}
	if j.Binding != serverBinding(v) {
		return j, errors.New("SSH 连接信息已改变，请重新保存凭据")
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM server_operations WHERE server_id=? AND state IN ('pending','running','uncertain') AND (kind!='agent-upgrade' OR ?!='agent-upgrade' OR node_id=?)", id, kind, node).Scan(&count); err != nil {
		return j, err
	}
	if count > 0 {
		return j, errors.New("服务器已有同类或冲突的运维任务")
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM jobs j JOIN nodes n ON n.id=j.node_id WHERE json_extract(n.config,'$.serverId')=? AND j.state IN ('pending','running','cancelling')", id).Scan(&count); err != nil {
		return j, err
	}
	if count > 0 {
		return j, errors.New("服务器正在部署或清理，请完成后再操作")
	}
	if kind == "agent-upgrade" {
		var raw, lifecycle string
		var managed int
		if err = tx.QueryRow("SELECT config,lifecycle,managed FROM nodes WHERE id=?", node).Scan(&raw, &lifecycle, &managed); err != nil {
			return j, notFound(err)
		}
		var n model.Node
		if err = json.Unmarshal([]byte(raw), &n); err != nil {
			return j, err
		}
		if n.ServerID != id || lifecycle != "active" || managed != 1 {
			return j, errors.New("只能升级此服务器上已托管的活动节点")
		}
	}
	if _, err = tx.Exec("INSERT INTO server_operations("+operationColumns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", j.ID, j.ServerID, j.NodeID, j.Kind, j.State, j.CredentialRevision, j.Binding, j.Secret, j.Target, j.Message, j.CreatedAt, 0, 0); err != nil {
		return j, err
	}
	if kind == "check" {
		if _, err = tx.Exec("UPDATE server_credentials SET next_check=? WHERE server_id=?", s.Now().Add(time.Hour).Unix(), id); err != nil {
			return j, err
		}
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,?,?)", s.Now().Unix(), "server."+kind+".queued", id+"/"+j.ID); err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) ScheduleCredentialChecks() error {
	rows, err := s.DB.Query("SELECT c.server_id FROM server_credentials c JOIN servers s ON s.id=c.server_id WHERE s.deleted_at=0 AND c.next_check<=? ORDER BY c.next_check LIMIT 100", s.Now().Unix())
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		_, _ = s.QueueServerOperation(id, "", "check", "", "", 0)
	}
	return nil
}
func (s *Store) RecoverServerOperations() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE server_operations SET state='failed',secret='',finished_at=?,message='远程改密已停用，请更新后台保存的 SSH 密码' WHERE kind='password' AND state IN ('pending','running','uncertain')", s.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE server_operations SET state='pending',message='服务重启，等待核对上次操作结果' WHERE state='running' AND kind IN ('check','agent-upgrade')"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) NextServerOperation() (ServerOperation, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return ServerOperation{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRow(`SELECT ` + operationColumns + ` FROM server_operations o WHERE state='pending' AND kind IN ('check','agent-upgrade')
AND NOT EXISTS(SELECT 1 FROM server_operations b WHERE b.server_id=o.server_id AND b.state IN ('running','uncertain'))
AND NOT EXISTS(SELECT 1 FROM jobs j JOIN nodes n ON n.id=j.node_id WHERE json_extract(n.config,'$.serverId')=o.server_id AND j.state IN ('running','cancelling')) ORDER BY created_at,id LIMIT 1`)
	j, err := scanOperation(row)
	if err != nil {
		return j, err
	}
	result, err := tx.Exec("UPDATE server_operations SET state='running',started_at=? WHERE id=? AND state='pending'", s.Now().Unix(), j.ID)
	if err != nil {
		return j, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return j, ErrConflict
	}
	j.State = "running"
	j.StartedAt = s.Now().Unix()
	return j, tx.Commit()
}
func (s *Store) OperationCredentials(j ServerOperation) (model.Server, string, string, error) {
	v, err := s.Server(j.ServerID)
	if err != nil {
		return v, "", "", err
	}
	var secret, binding string
	var revision int64
	if err = s.DB.QueryRow("SELECT secret,binding,revision FROM server_credentials WHERE server_id=?", v.ID).Scan(&secret, &binding, &revision); err != nil {
		return v, "", "", notFound(err)
	}
	if revision != j.CredentialRevision || binding != j.Binding || binding != serverBinding(v) {
		return v, "", "", ErrConflict
	}
	old, err := s.Vault.Decrypt(secret)
	if err != nil {
		return v, "", "", secretError(err)
	}
	next, err := s.Vault.Decrypt(j.Secret)
	return v, old, next, secretError(err)
}
func (s *Store) FinishServerOperation(j ServerOperation, state, message, credentialStatus string) error {
	if state != "succeeded" && state != "failed" {
		return errors.New("无效任务结果")
	}
	if j.Kind != "check" && j.Kind != "agent-upgrade" {
		state, message, credentialStatus = "failed", "远程改密已停用，请更新后台保存的 SSH 密码", ""
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRow("SELECT state FROM server_operations WHERE id=?", j.ID).Scan(&current); err != nil {
		return err
	}
	// A successful commit may have lost its response. Do not duplicate its
	// audit record when only the acknowledgement is retried.
	if current == state {
		return nil
	}
	if current != "running" {
		return ErrConflict
	}
	previousCredentialStatus := ""
	if j.Kind == "check" {
		if err = tx.QueryRow("SELECT status FROM server_credentials WHERE server_id=?", j.ServerID).Scan(&previousCredentialStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if credentialStatus != "" && j.Kind == "check" {
		if _, err = tx.Exec("UPDATE server_credentials SET status=?,message=?,last_checked=?,next_check=? WHERE server_id=? AND revision=? AND binding=?", credentialStatus, message, s.Now().Unix(), s.Now().Add(time.Hour).Unix(), j.ServerID, j.CredentialRevision, j.Binding); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE server_operations SET state=?,message=?,secret=?,finished_at=? WHERE id=?", state, message, "", s.Now().Unix(), j.ID); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,?,?)", s.Now().Unix(), "server."+j.Kind+"."+state, j.ServerID+"/"+j.ID); err != nil {
		return err
	}
	if err = s.bestEffortOperationLog(tx, func(ctx context.Context) error {
		var manual int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_job_context WHERE job_id=?", j.ID).Scan(&manual); err != nil {
			return err
		}
		// Routine healthy credential checks are noise. Manual checks and changes
		// of health retain both the operator and the terminal result.
		record := j.Kind != "check" || manual != 0 || (state == "failed" && previousCredentialStatus != credentialStatus) || (state == "succeeded" && previousCredentialStatus != "" && previousCredentialStatus != "unchecked" && previousCredentialStatus != "healthy")
		if !record {
			return nil
		}
		result := "success"
		if state == "failed" {
			result = "failure"
		}
		return jobOperationLog(ctx, tx, model.OperationLog{ID: "server-finished:" + j.ID, At: s.Now(), Category: "operations", Action: "server." + j.Kind + "." + state, ResourceType: "server", ResourceID: j.ServerID, JobID: j.ID, Result: result, DurationMS: max(0, s.Now().Unix()-j.StartedAt) * 1000, Details: map[string]any{"nodeId": j.NodeID, "status": credentialStatus}}, s.Now())
	}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RetryServerOperation(id string) error {
	return errors.New("远程改密任务已停用，请更新后台保存的 SSH 密码")
}

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrQualityCooldown = errors.New("网络质量检测每 10 分钟最多运行一次，请稍后重试")

func EnsureNetworkQualitySchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS node_network_quality(
 node_id TEXT PRIMARY KEY REFERENCES nodes(id),status TEXT NOT NULL,job_id INTEGER NOT NULL,
 source_ip TEXT NOT NULL,requested_at INTEGER NOT NULL,started_at INTEGER NOT NULL DEFAULT 0,
 finished_at INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',report_at INTEGER NOT NULL DEFAULT 0,
 report_ip TEXT NOT NULL DEFAULT '',report TEXT NOT NULL DEFAULT 'null');`)
	return err
}

func (s *Store) NetworkQuality(id string) (model.NetworkQualityStatus, error) {
	v := model.NetworkQualityStatus{NodeID: id, Status: "not-run"}
	var raw string
	var requested, started, finished, reportAt int64
	err := s.DB.QueryRow(`SELECT status,job_id,source_ip,requested_at,started_at,finished_at,error,report_at,report_ip,report
 FROM node_network_quality WHERE node_id=?`, id).Scan(&v.Status, &v.JobID, &v.SourceIP, &requested, &started, &finished, &v.Error, &reportAt, &v.ReportSourceIP, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		err = s.DB.QueryRow("SELECT 1 FROM nodes WHERE id=?", id).Scan(&exists)
		return v, notFound(err)
	}
	if err != nil {
		return v, err
	}
	v.RequestedAt, v.StartedAt, v.FinishedAt, v.ReportAt = alertTime(requested), alertTime(started), alertTime(finished), alertTime(reportAt)
	v.Report = json.RawMessage(raw)
	return v, nil
}

// QueueNetworkQuality uses the same durable job and server lock as deployment.
// It changes neither the node configuration nor its proxy readiness.
func (s *Store) QueueNetworkQuality(id, password string, revision int64) (model.NetworkQualityStatus, error) {
	if password == "" || len(password) > 1024 {
		return model.NetworkQualityStatus{}, errors.New("请填写本次检测使用的 SSH 密码")
	}
	enc, err := s.Vault.Encrypt(password)
	if err != nil {
		return model.NetworkQualityStatus{}, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return model.NetworkQualityStatus{}, err
	}
	defer tx.Rollback()
	var lifecycle, raw string
	var actualRevision int64
	if err = tx.QueryRow("SELECT config,lifecycle,revision FROM nodes WHERE id=?", id).Scan(&raw, &lifecycle, &actualRevision); err != nil {
		return model.NetworkQualityStatus{}, notFound(err)
	}
	if lifecycle != "active" || actualRevision != revision {
		return model.NetworkQualityStatus{}, ErrConflict
	}
	var node model.Node
	if err = json.Unmarshal([]byte(raw), &node); err != nil {
		return model.NetworkQualityStatus{}, err
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM jobs WHERE node_id=? AND state IN ('pending','running','cancelling')", id).Scan(&count); err != nil {
		return model.NetworkQualityStatus{}, err
	}
	if count != 0 {
		return model.NetworkQualityStatus{}, errors.New("节点已有任务，请等待当前任务完成")
	}
	var requested int64
	err = tx.QueryRow("SELECT requested_at FROM node_network_quality WHERE node_id=?", id).Scan(&requested)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.NetworkQualityStatus{}, err
	}
	if requested > 0 && s.Now().Unix()-requested < 600 {
		return model.NetworkQualityStatus{}, ErrQualityCooldown
	}
	res, err := tx.Exec("INSERT INTO jobs(node_id,kind,created_at) VALUES(?,'quality',?)", id, s.Now().Unix())
	if err != nil {
		return model.NetworkQualityStatus{}, err
	}
	jobID, err := res.LastInsertId()
	if err != nil {
		return model.NetworkQualityStatus{}, err
	}
	if _, err = tx.Exec("UPDATE nodes SET password=?,password_expires=? WHERE id=?", enc, s.Now().Add(24*time.Hour).Unix(), id); err != nil {
		return model.NetworkQualityStatus{}, err
	}
	if _, err = tx.Exec(`INSERT INTO node_network_quality(node_id,status,job_id,source_ip,requested_at) VALUES(?,'queued',?,?,?)
 ON CONFLICT(node_id) DO UPDATE SET status='queued',job_id=excluded.job_id,source_ip=excluded.source_ip,requested_at=excluded.requested_at,started_at=0,finished_at=0,error=''`, id, jobID, node.PublicIP, s.Now().Unix()); err != nil {
		return model.NetworkQualityStatus{}, err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'node.network-quality',?)", s.Now().Unix(), id); err != nil {
		return model.NetworkQualityStatus{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.NetworkQualityStatus{}, err
	}
	return s.NetworkQuality(id)
}

func (s *Store) StartNetworkQuality(id string, jobID int64, ip string) error {
	res, err := s.DB.Exec(`INSERT INTO node_network_quality(node_id,status,job_id,source_ip,requested_at,started_at)
 SELECT ?,'running',?,?,?,? WHERE EXISTS(SELECT 1 FROM jobs j JOIN nodes n ON n.id=j.node_id WHERE j.id=? AND j.node_id=? AND j.state='running' AND n.lifecycle='active')
 ON CONFLICT(node_id) DO UPDATE SET status='running',job_id=excluded.job_id,source_ip=excluded.source_ip,
 requested_at=CASE WHEN node_network_quality.job_id=excluded.job_id THEN node_network_quality.requested_at ELSE excluded.requested_at END,started_at=excluded.started_at,finished_at=0,error=''`, id, jobID, ip, s.Now().Unix(), s.Now().Unix(), jobID, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) FinishNetworkQuality(id string, jobID int64, status, message string, report json.RawMessage) error {
	if status != "complete" && status != "failed" && status != "cancelled" {
		return errors.New("检测状态无效")
	}
	if status == "complete" && (len(report) == 0 || len(report) > 512<<10 || !json.Valid(report) || report[0] != '{') {
		return errors.New("检测报告无效")
	}
	_, err := s.DB.Exec(`UPDATE node_network_quality SET
 status=CASE WHEN EXISTS(SELECT 1 FROM nodes WHERE id=? AND lifecycle!='active') THEN 'cancelled' ELSE ? END,
 error=?,finished_at=?,report=CASE WHEN ?='complete' AND EXISTS(SELECT 1 FROM nodes WHERE id=node_network_quality.node_id AND lifecycle='active') THEN ? ELSE report END,
 report_at=CASE WHEN ?='complete' AND EXISTS(SELECT 1 FROM nodes WHERE id=node_network_quality.node_id AND lifecycle='active') THEN ? ELSE report_at END,
 report_ip=CASE WHEN ?='complete' AND EXISTS(SELECT 1 FROM nodes WHERE id=node_network_quality.node_id AND lifecycle='active') THEN source_ip ELSE report_ip END
 WHERE node_id=? AND job_id=? AND status IN ('running','queued')`, id, status, message, s.Now().Unix(), status, string(report), status, s.Now().Unix(), status, id, jobID)
	return err
}

// A shutdown never silently restarts a scan with stale SSH credentials. Install
// recovery remains owned by RecoverJobs; a fresh explicit scan can be queued.
func (s *Store) RecoverNetworkQuality() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE nodes SET password='',password_expires=0 WHERE id IN (SELECT node_id FROM jobs WHERE kind='quality' AND state IN ('pending','running','cancelling'))"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE jobs SET state='cancelled' WHERE kind='quality' AND state IN ('pending','running','cancelling')"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE node_network_quality SET status='cancelled',error='控制服务重启，检测已中断，请重新发起',finished_at=? WHERE status IN ('queued','running')", s.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

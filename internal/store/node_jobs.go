package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/KeepS13/HQIP/internal/model"
)

type Job struct {
	ID                   int64
	NodeID, Kind         string
	Takeover             bool   `json:"takeover,omitempty"`
	PreflightFingerprint string `json:"preflightFingerprint,omitempty"`
	CertificateMode      string `json:"certificateMode,omitempty"`
	ZoneID               string `json:"zoneId,omitempty"`
}

func (s *Store) RecoverJobs() error {
	for _, query := range []string{
		"UPDATE jobs SET state='pending' WHERE state='running'",
		"UPDATE jobs SET state='cancelled' WHERE state='cancelling'",
	} {
		if _, err := s.DB.Exec(query); err != nil {
			return err
		}
	}
	return nil
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
AND NOT EXISTS(SELECT 1 FROM jobs busy JOIN nodes bn ON bn.id=busy.node_id WHERE busy.state IN ('running','cancelling') AND json_extract(bn.config,'$.serverId')=json_extract(n.config,'$.serverId'))
AND NOT EXISTS(SELECT 1 FROM server_operations op WHERE op.server_id=json_extract(n.config,'$.serverId') AND op.state IN ('running','uncertain')) ORDER BY j.id LIMIT 1`).Scan(&j.ID, &j.NodeID, &j.Kind, &options)
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

func (s *Store) FinishJob(j Job, success bool) error {
	state := "error"
	if success {
		state = "done"
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRow("UPDATE jobs SET state=CASE WHEN state='cancelling' THEN 'cancelled' ELSE ? END WHERE id=? AND state IN ('running','cancelling') RETURNING state", state, j.ID).Scan(&state)
	changed := err == nil
	if err == nil && state == "error" {
		err = recordJobNotification(tx, j.ID, s.Now().Unix())
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if changed {
		if err = s.bestEffortOperationLog(tx, func(ctx context.Context) error {
			result := "failure"
			if state == "done" {
				result = "success"
			}
			logState := state
			if j.Kind == "quality" {
				var qualityState string
				if err := tx.QueryRowContext(ctx, "SELECT status FROM node_network_quality WHERE node_id=? AND job_id=?", j.NodeID, j.ID).Scan(&qualityState); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if qualityState != "complete" {
					result = "failure"
				}
				if qualityState != "" {
					logState = qualityState
				}
			}
			jobID := nodeOperationJobID(j.ID)
			return jobOperationLog(ctx, tx, model.OperationLog{ID: "job-finished:" + jobID, At: s.Now(), Category: "operations", Action: "node." + j.Kind + "." + logState, ResourceType: "node", ResourceID: j.NodeID, JobID: jobID, Result: result, Details: map[string]any{"status": logState}}, s.Now())
		}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// If no owner was ever installed, cancellation has no remote resources to
	// retire. Managed partial installs remain visible until stopped/cleaned.
	var abandoned int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=? AND lifecycle='retiring' AND managed=0 AND NOT EXISTS(SELECT 1 FROM jobs WHERE node_id=? AND state IN ('running','cancelling'))", j.NodeID, j.NodeID).Scan(&abandoned); err != nil {
		return err
	}
	if abandoned == 1 {
		return s.CompleteNodeRetirement(j.NodeID)
	}
	return nil
}

func (s *Store) Queue(id, kind, password string) (model.Node, error) {
	if kind != "install" && kind != "verify" {
		return model.Node{}, errors.New("任务类型无效")
	}
	return s.queueWithOptions(id, kind, password, Job{})
}

func (s *Store) CheckIntegrity() error {
	if s.postgresDSN != "" {
		var invalid int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT i.indisvalid").Scan(&invalid); err != nil {
			return err
		}
		if invalid > 0 {
			return errors.New("PostgreSQL 存在无效索引")
		}
	} else {
		var result string
		if err := s.DB.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return errors.New("数据库完整性检查失败")
		}
	}
	return validateVault(context.Background(), s.DB, s.Vault)
}

package store

import (
	"errors"
	"strconv"
	"time"
)

type RecoveryUser struct {
	UserID           string `json:"userId"`
	UsedBytes        int64  `json:"usedBytes"`
	ReservedBytes    int64  `json:"reservedBytes"`
	MinimumUsedBytes int64  `json:"minimumUsedBytes"`
}
type PortalRecovery struct {
	Hold      bool           `json:"hold"`
	Epoch     string         `json:"recoveryEpoch"`
	StartedAt time.Time      `json:"startedAt"`
	Ready     bool           `json:"ready"`
	Reason    string         `json:"reason"`
	Users     []RecoveryUser `json:"users"`
}
type RecoveryTotal struct {
	UserID    string `json:"userId"`
	UsedBytes int64  `json:"usedBytes"`
}

func (s *Store) PortalRecovery() (PortalRecovery, error) {
	v := PortalRecovery{Hold: s.PortalSetting("portal_recovery_hold") == "true", Epoch: s.PortalSetting("portal_recovery_epoch"), Users: []RecoveryUser{}}
	at, _ := strconv.ParseInt(s.PortalSetting("portal_recovery_started"), 10, 64)
	v.StartedAt = time.Unix(at, 0).UTC()
	v.Ready = v.Hold && s.Now().Unix() >= at+120
	if !v.Ready {
		v.Reason = "等待旧连接租约失效至少120秒"
	}
	var missing int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM nodes WHERE lifecycle='active' AND (last_seen<=? OR applied_revision<desired_revision OR core_healthy=0 OR last_seen<?)", at, s.Now().Add(-30*time.Second).Unix()).Scan(&missing); err != nil {
		return v, err
	}
	if missing > 0 {
		v.Ready = false
		v.Reason = "仍有节点未提交恢复后的在线配置确认与最终账本"
	}
	rows, err := s.DB.Query(`SELECT u.id,x.upload+x.download,x.reserved,MAX(0,COALESCE((SELECT SUM(l.upload+l.download) FROM lifetime l WHERE l.device_id='user_'||u.id),0)-COALESCE((SELECT SUM(p.upload+p.download) FROM portal_usage p WHERE p.user_id=u.id),0)) FROM portal_users u JOIN portal_usage x ON x.user_id=u.id AND x.epoch=u.epoch`)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RecoveryUser
		var gap int64
		if err = rows.Scan(&r.UserID, &r.UsedBytes, &r.ReservedBytes, &gap); err != nil {
			return v, err
		}
		r.MinimumUsedBytes = r.UsedBytes + gap
		v.Users = append(v.Users, r)
	}
	if v.Ready {
		v.Reason = "需要管理员核对所有节点最终账本及未结算预算，确认累计量后恢复；不会自动归还预留额度"
	}
	return v, rows.Err()
}
func (s *Store) ResumePortalRecovery(expected string, confirmed bool, totals []RecoveryTotal) error {
	state, err := s.PortalRecovery()
	if err != nil {
		return err
	}
	if !state.Hold || !state.Ready || !confirmed || expected == "" || expected != state.Epoch {
		return errors.New("恢复核对条件尚未满足")
	}
	set := map[string]int64{}
	for _, v := range totals {
		if v.UserID == "" || v.UsedBytes < 0 || v.UsedBytes > 1<<60 {
			return errors.New("恢复用量无效")
		}
		if _, ok := set[v.UserID]; ok {
			return errors.New("恢复用户重复")
		}
		set[v.UserID] = v.UsedBytes
	}
	if len(set) != len(state.Users) {
		return errors.New("必须逐一核对全部用户的最终账本")
	}
	for _, u := range state.Users {
		value, ok := set[u.UserID]
		if !ok || value < u.MinimumUsedBytes {
			return errors.New("恢复累计量不得低于已知最终账本")
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var epoch, hold string
	if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_recovery_epoch'").Scan(&epoch); err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT value FROM metadata WHERE key='portal_recovery_hold'").Scan(&hold); err != nil {
		return err
	}
	if epoch != expected || hold != "true" {
		return ErrConflict
	}
	var nodeGaps, userCount int
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE lifecycle='active' AND (last_seen<=? OR applied_revision<desired_revision OR core_healthy=0 OR last_seen<?)", state.StartedAt.Unix(), s.Now().Add(-30*time.Second).Unix()).Scan(&nodeGaps); err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM portal_users").Scan(&userCount); err != nil {
		return err
	}
	if nodeGaps != 0 || userCount != len(set) || s.Now().Before(state.StartedAt.Add(120*time.Second)) {
		return ErrConflict
	}
	for id, target := range set {
		var used, min int64
		if err = tx.QueryRow(`SELECT x.upload+x.download,x.upload+x.download+MAX(0,COALESCE((SELECT SUM(l.upload+l.download) FROM lifetime l WHERE l.device_id='user_'||u.id),0)-COALESCE((SELECT SUM(p.upload+p.download) FROM portal_usage p WHERE p.user_id=u.id),0)) FROM portal_users u JOIN portal_usage x ON x.user_id=u.id AND x.epoch=u.epoch WHERE u.id=?`, id).Scan(&used, &min); err != nil {
			return err
		}
		if target < min {
			return ErrConflict
		}
		if _, err = tx.Exec("UPDATE portal_usage SET download=download+? WHERE user_id=? AND epoch=(SELECT epoch FROM portal_users WHERE id=?)", target-used, id, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE metadata SET value='false' WHERE key='portal_recovery_hold'"); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'portal.recovery_resumed',?)", s.Now().Unix(), expected); err != nil {
		return err
	}
	return tx.Commit()
}

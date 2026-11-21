package store

import (
	"errors"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type RecoveryUser struct {
	Periods          []model.SubscriptionPeriod `json:"periods"`
	SubscriptionID   string                     `json:"subscriptionId"`
	UserID           string                     `json:"userId"`
	UsedBytes        int64                      `json:"usedBytes"`
	ReservedBytes    int64                      `json:"reservedBytes"`
	MinimumUsedBytes int64                      `json:"minimumUsedBytes"`
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
	PeriodAdjustments []RecoveryPeriodAdjustment `json:"periodAdjustments,omitempty"`
	SubscriptionID    string                     `json:"subscriptionId"`
	UserID            string                     `json:"userId"`
	UsedBytes         int64                      `json:"usedBytes"`
}
type RecoveryPeriodAdjustment struct {
	PeriodID            string `json:"periodId"`
	AdditionalUsedBytes int64  `json:"additionalUsedBytes"`
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
	rows, err := s.DB.Query(`SELECT p.user_id,p.id,x.upload+x.download,x.reserved,MAX(0,COALESCE((SELECT SUM(l.upload+l.download) FROM lifetime l WHERE l.device_id='subscription_'||p.id),0)-x.upload-x.download) FROM portal_subscriptions p JOIN portal_usage x ON x.subscription_id=p.id`)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RecoveryUser
		var gap int64
		if err = rows.Scan(&r.UserID, &r.SubscriptionID, &r.UsedBytes, &r.ReservedBytes, &gap); err != nil {
			return v, err
		}
		r.MinimumUsedBytes = r.UsedBytes + gap
		v.Users = append(v.Users, r)
	}
	if err = rows.Err(); err != nil {
		return v, err
	}
	rows.Close()
	for i := range v.Users {
		e := &model.UserSubscription{ID: v.Users[i].SubscriptionID}
		if err = readSubscriptionPeriods(s.DB, e, s.Now()); err != nil {
			return v, err
		}
		v.Users[i].Periods = e.Periods
	}
	if v.Ready {
		v.Reason = "需要管理员核对所有节点最终账本及未结算预算，确认累计量后恢复；不会自动归还预留额度"
	}
	return v, nil
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
	adjustments := map[string][]RecoveryPeriodAdjustment{}
	for _, v := range totals {
		if v.SubscriptionID == "" || v.UsedBytes < 0 || v.UsedBytes > 1<<60 {
			return errors.New("恢复用量无效")
		}
		if _, ok := set[v.SubscriptionID]; ok {
			return errors.New("恢复订阅重复")
		}
		set[v.SubscriptionID] = v.UsedBytes
		adjustments[v.SubscriptionID] = v.PeriodAdjustments
	}
	if len(set) != len(state.Users) {
		return errors.New("必须逐一核对全部订阅的最终账本")
	}
	for _, u := range state.Users {
		value, ok := set[u.SubscriptionID]
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
	if err = tx.QueryRow("SELECT COUNT(*) FROM portal_subscriptions").Scan(&userCount); err != nil {
		return err
	}
	if nodeGaps != 0 || userCount != len(set) || s.Now().Before(state.StartedAt.Add(120*time.Second)) {
		return ErrConflict
	}
	for id, target := range set {
		var used, min int64
		if err = tx.QueryRow(`SELECT x.upload+x.download,MAX(x.upload+x.download,COALESCE((SELECT SUM(l.upload+l.download) FROM lifetime l WHERE l.device_id='subscription_'||p.id),0)) FROM portal_subscriptions p JOIN portal_usage x ON x.subscription_id=p.id WHERE p.id=?`, id).Scan(&used, &min); err != nil {
			return err
		}
		if target < min {
			return ErrConflict
		}
		delta := target - used
		parts := adjustments[id]
		if len(parts) == 0 && delta > 0 {
			var count int
			if err = tx.QueryRow("SELECT COUNT(*) FROM subscription_periods WHERE subscription_id=?", id).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return errors.New("多周期订阅的恢复差额必须逐项指定所属周期，不能直接扣入当前期")
			}
			parts = []RecoveryPeriodAdjustment{{PeriodID: id, AdditionalUsedBytes: delta}}
		}
		var sum int64
		seen := map[string]bool{}
		for _, part := range parts {
			if seen[part.PeriodID] || part.AdditionalUsedBytes < 0 || part.AdditionalUsedBytes > delta-sum {
				return errors.New("恢复周期差额无效")
			}
			seen[part.PeriodID] = true
			sum += part.AdditionalUsedBytes
			result, e := tx.Exec("UPDATE subscription_periods SET download=download+? WHERE id=? AND subscription_id=? AND starts_at<=?", part.AdditionalUsedBytes, part.PeriodID, id, s.Now().Unix())
			if e != nil {
				return e
			}
			if n, e := result.RowsAffected(); e != nil {
				return e
			} else if n != 1 {
				return errors.New("恢复周期不属于此订阅或尚未开始")
			}
		}
		if sum != delta {
			return errors.New("恢复周期差额之和必须等于累计差额")
		}
		if _, err = tx.Exec("UPDATE portal_usage SET download=download+? WHERE subscription_id=?", target-used, id); err != nil {
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

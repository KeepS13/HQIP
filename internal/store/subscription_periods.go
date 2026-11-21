package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func readSubscriptionPeriods(q portalQuery, e *model.UserSubscription, now time.Time) error {
	rows, err := q.Query("SELECT id,starts_at,ends_at,order_id,upload,download,reserved FROM subscription_periods WHERE subscription_id=? ORDER BY starts_at,id", e.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	e.Periods = []model.SubscriptionPeriod{}
	for rows.Next() {
		var p model.SubscriptionPeriod
		var start, end int64
		if err = rows.Scan(&p.ID, &start, &end, &p.OrderID, &p.UploadBytes, &p.DownloadBytes, &p.ReservedBytes); err != nil {
			return err
		}
		p.StartsAt, p.EndsAt = time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
		e.Periods = append(e.Periods, p)
		if start <= now.Unix() || e.CurrentPeriodID == "" {
			e.CurrentPeriodID = p.ID
			e.CurrentPeriodStartsAt, e.CurrentPeriodEndsAt = p.StartsAt, p.EndsAt
			// Before the first rollover, the lifetime ledger is also the current
			// ledger. This includes historical recovery adjustments.
			if p.OrderID != "" {
				e.UsedBytes, e.ReservedBytes = p.UploadBytes+p.DownloadBytes, p.ReservedBytes
			}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if e.CurrentPeriodID == "" {
		return errors.New("订阅周期记录缺失或服务器时间异常")
	}
	if e.CurrentPeriodEndsAt.Before(e.ExpiresAt) && e.CurrentPeriodEndsAt.After(now) {
		at := e.CurrentPeriodEndsAt
		e.NextResetAt = &at
	}
	return nil
}

func leasePeriod(q portalQuery, lease string) (string, error) {
	var period string
	err := q.QueryRow("SELECT period_id FROM subscription_lease_periods WHERE lease_id=?", lease).Scan(&period)
	return period, err
}

// Both ledgers change in the same subscription-scoped transaction. A late
// report uses its lease's immutable period, never the server's current period.
func updatePeriodUsage(tx *sql.Tx, subscription, period string, upload, download, reserved int64) error {
	result, err := tx.Exec("UPDATE subscription_periods SET upload=upload+?,download=download+?,reserved=reserved+? WHERE id=? AND subscription_id=?", upload, download, reserved, period, subscription)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return errors.New("订阅周期记录不存在")
	}
	_, err = tx.Exec("UPDATE portal_usage SET upload=upload+?,download=download+?,reserved=reserved+? WHERE subscription_id=?", upload, download, reserved, subscription)
	return err
}

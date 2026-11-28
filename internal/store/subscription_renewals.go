package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrSubscriptionRenewal = errors.New("该订阅不可续费：须本人持有、已开通且未到期，订阅及套餐启用并有定价")
var ErrSubscriptionPeriodExpiry = errors.New("到期时间必须晚于最后一期的开始时间，不能截断已续费周期")

func validateRenewalOrder(tx *sql.Tx, user string, in model.PaymentOrderInput, now time.Time) (*model.UserSubscription, error) {
	e, err := userSubscription(tx, user, in.RenewalSubscriptionID, now)
	if err != nil {
		return nil, err
	}
	if e.TemplateID != in.TemplateID || !e.Renewable {
		return nil, ErrSubscriptionRenewal
	}
	if e.Revision != in.ExpectedSubscriptionRevision {
		return nil, ErrConflict
	}
	return e, nil
}

// Caller holds the user's allocation lock, including in payment notification
// handling. Receipt, extension and fulfillment commit atomically; no provider
// request is made here. A delayed worker cannot turn a timely receipt into an
// expired renewal, and a late receipt can never resurrect the subscription.
// Retain the original grant key and every surviving node. Any replacements
// and lease revocations commit with the new period; a shortage only records
// the blocked payment and leaves the existing entitlement intact.
func (s *Store) fulfillPaymentRenewal(tx *sql.Tx, r paymentOrderRecord, now time.Time) (bool, error) {
	o := r.Order
	claimed, err := tx.Exec("UPDATE payment_orders SET revision=revision WHERE id=? AND payment_state='paid' AND fulfillment_state!='fulfilled'", o.ID)
	if err != nil {
		return false, err
	}
	if n, err := claimed.RowsAffected(); err != nil {
		return false, err
	} else if n != 1 {
		return false, nil
	}
	var user, template string
	var expires, revision int64
	var revoked, enabled bool
	err = tx.QueryRow("SELECT s.user_id,s.template_id,s.expires,s.revision,s.revoked,u.enabled FROM portal_subscriptions s JOIN portal_users u ON u.id=s.user_id WHERE s.id=?", o.RenewalSubscriptionID).Scan(&user, &template, &expires, &revision, &revoked, &enabled)
	if err != nil {
		return false, err
	}
	t, err := subscriptionTemplate(tx, o.TemplateID)
	if err != nil {
		return false, err
	}
	message := ""
	switch {
	case user != o.UserID || template != o.TemplateID:
		message = "续费目标不符合条件，已付款订单等待管理员处理"
	case revoked:
		message = "订阅已移除，已付款订单等待管理员处理"
	case expires <= now.Unix():
		message = "付款核验时订阅已到期，不能续费；已收款，等待管理员处理"
	case !enabled || !t.Enabled:
		message = "用户或套餐已停用，已付款续费订单等待管理员处理"
	}
	var replacements []string
	if message == "" {
		replacements, err = renewalReplacementNodes(tx, user, o.RenewalSubscriptionID, t, now)
		if errors.Is(err, ErrRenewalNodes) {
			message = err.Error() + "。已收到付款，请勿重复支付"
		} else if err != nil {
			return false, err
		}
	}
	if message != "" {
		if _, err = tx.Exec("UPDATE payment_orders SET fulfillment_state='blocked',last_error=?,revision=revision+1 WHERE id=?", message, o.ID); err != nil {
			return false, err
		}
		return false, paymentActivity(tx, o.ID, "renewal.blocked", message, now.Unix())
	}
	var previousPeriod string
	var end int64
	if err = tx.QueryRow("SELECT id,ends_at FROM subscription_periods WHERE subscription_id=? ORDER BY starts_at DESC LIMIT 1", o.RenewalSubscriptionID).Scan(&previousPeriod, &end); err != nil {
		return false, err
	}
	if end != expires || o.ValidityDays < 1 || o.ValidityDays > 36500 {
		return false, errors.New("续费周期记录不一致")
	}
	// Reconcile the original ledger before its first rollover, including any
	// pre-upgrade or recovery adjustments, without touching historical periods.
	if previousPeriod == o.RenewalSubscriptionID {
		if _, err = tx.Exec(`UPDATE subscription_periods SET upload=(SELECT upload FROM portal_usage WHERE subscription_id=?),download=(SELECT download FROM portal_usage WHERE subscription_id=?),reserved=(SELECT reserved FROM portal_usage WHERE subscription_id=?) WHERE id=?`, o.RenewalSubscriptionID, o.RenewalSubscriptionID, o.RenewalSubscriptionID, previousPeriod); err != nil {
			return false, err
		}
	}
	period := NewID()
	next := time.Unix(expires, 0).UTC().Add(time.Duration(o.ValidityDays) * 24 * time.Hour)
	if next.Year() > 9999 {
		return false, errors.New("订阅到期时间超出支持范围")
	}
	if _, err = tx.Exec("INSERT INTO subscription_periods(id,subscription_id,starts_at,ends_at,order_id) VALUES(?,?,?,?,?)", period, o.RenewalSubscriptionID, expires, next.Unix(), o.ID); err != nil {
		return false, err
	}
	result, err := tx.Exec("UPDATE portal_subscriptions SET expires=?,revision=revision+1 WHERE id=? AND revision=? AND revoked=0 AND expires>?", next.Unix(), o.RenewalSubscriptionID, revision, now.Unix())
	if err != nil {
		return false, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return false, err
	} else if n != 1 {
		return false, ErrConflict
	}
	if replacements != nil {
		if _, err = tx.Exec("UPDATE subscription_allocations SET node_ids=? WHERE subscription_id=?", jsonString(replacements), o.RenewalSubscriptionID); err != nil {
			return false, err
		}
		updated, err := userSubscription(tx, user, o.RenewalSubscriptionID, now)
		if err != nil {
			return false, err
		}
		if err = reconcileSubscriptionLeases(tx, updated, now); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec("UPDATE payment_renewals SET period_id=? WHERE order_id=?", period, o.ID); err != nil {
		return false, err
	}
	if _, err = tx.Exec("UPDATE payment_orders SET fulfillment_state='fulfilled',fulfilled_at=?,subscription_id=?,last_error='',revision=revision+1 WHERE id=?", now.Unix(), o.RenewalSubscriptionID, o.ID); err != nil {
		return false, err
	}
	if _, err = tx.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user); err != nil {
		return false, err
	}
	if err = paymentActivity(tx, o.ID, "renewal.fulfilled", "续费成功，到期时间已延长；本期流量不变，下一期开始时重置", now.Unix()); err != nil {
		return false, err
	}
	details := map[string]any{"subscriptionId": o.RenewalSubscriptionID, "periodId": period, "startsAt": time.Unix(expires, 0).UTC(), "endsAt": next}
	if replacements != nil {
		details["nodeIds"] = replacements
	}
	if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'subscription.renewed',?)", now.Unix(), jsonString(details)); err != nil {
		return false, err
	}
	err = s.bestEffortOperationLog(tx, func(ctx context.Context) error {
		return appendOperationLog(ctx, tx, model.OperationLog{ID: "payment-renewed:" + o.ID, At: now, Category: "payment", Action: "subscription.renewed", ActorType: "system", ActorName: "系统", AuthType: "system", ResourceType: "order", ResourceID: o.ID, Result: "success", Details: details}, now, false)
	})
	return true, err
}

package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type paymentOrderRecord struct {
	Order            model.PaymentOrder
	ConfigVersion    int64
	TemplateRevision int64
	ProviderIdentity string
	ClosedAt         int64
	PayURLEncrypted  string
	LastReconcileAt  int64
}

const paymentOrderFields = `o.id,o.order_no,o.user_id,u.email,o.template_id,o.template_name,o.template_snapshot,o.amount,o.validity_days,o.created_at,o.expires_at,o.paid_at,o.fulfilled_at,o.last_notify_at,o.closed_at,o.creation_state,o.payment_state,o.fulfillment_state,o.subscription_id,o.pay_url,o.platform_order_no,o.last_error,o.revision,o.config_version,o.template_revision,o.provider_identity,o.last_reconcile_at`
const paymentOrderSelect = `SELECT ` + paymentOrderFields + `,COALESCE(r.subscription_id,''),COALESCE(p.starts_at,0),COALESCE(p.ends_at,0) FROM payment_orders o JOIN portal_users u ON u.id=o.user_id LEFT JOIN payment_renewals r ON r.order_id=o.id LEFT JOIN subscription_periods p ON p.id=r.period_id `

type paymentScanner interface{ Scan(...any) error }

func scanPaymentOrder(row paymentScanner, now time.Time) (paymentOrderRecord, error) {
	var r paymentOrderRecord
	o := &r.Order
	var raw string
	var created, expires, paid, fulfilled, notify int64
	var periodStart, periodEnd int64
	err := row.Scan(&o.ID, &o.OrderNo, &o.UserID, &o.Email, &o.TemplateID, &o.TemplateName, &raw, &o.AmountCents, &o.ValidityDays, &created, &expires, &paid, &fulfilled, &notify, &r.ClosedAt, &o.CreationState, &o.PaymentState, &o.FulfillmentState, &o.SubscriptionID, &r.PayURLEncrypted, &o.PlatformOrderNo, &o.LastError, &o.Revision, &r.ConfigVersion, &r.TemplateRevision, &r.ProviderIdentity, &r.LastReconcileAt, &o.RenewalSubscriptionID, &periodStart, &periodEnd)
	if err != nil {
		return r, notFound(err)
	}
	if err = json.Unmarshal([]byte(raw), &o.TemplateSnapshot); err != nil {
		return r, err
	}
	o.CreatedAt = time.Unix(created, 0).UTC()
	o.ExpiresAt = time.Unix(expires, 0).UTC()
	o.Currency = "CNY"
	o.OrderType = "purchase"
	if o.RenewalSubscriptionID != "" {
		o.OrderType = "renewal"
	}
	if periodStart != 0 {
		start, end := time.Unix(periodStart, 0).UTC(), time.Unix(periodEnd, 0).UTC()
		o.PeriodStartsAt, o.PeriodEndsAt = &start, &end
	}
	if paid != 0 {
		at := time.Unix(paid, 0).UTC()
		o.PaidAt = &at
	}
	if fulfilled != 0 {
		at := time.Unix(fulfilled, 0).UTC()
		o.FulfilledAt = &at
	}
	if notify != 0 {
		at := time.Unix(notify, 0).UTC()
		o.LastNotifyAt = &at
	}
	o.Status = paymentStatus(*o, r.ClosedAt, now)
	return r, nil
}

func paymentStatus(o model.PaymentOrder, closed int64, now time.Time) string {
	if o.PaymentState == "paid" {
		if o.FulfillmentState == "fulfilled" {
			return "fulfilled"
		}
		return "paid_pending"
	}
	if closed != 0 {
		return "closed"
	}
	if !o.ExpiresAt.After(now) {
		return "expired"
	}
	switch o.CreationState {
	case "ready":
		return "pending"
	case "creating":
		return "creating"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

func paymentRecord(q portalQuery, id string, now time.Time) (paymentOrderRecord, error) {
	return scanPaymentOrder(q.QueryRow(paymentOrderSelect+"WHERE o.id=?", id), now)
}
func (s *Store) PaymentOrder(userID, id string) (model.PaymentOrder, error) {
	r, err := paymentRecord(s.DB, id, s.Now())
	if err != nil {
		return model.PaymentOrder{}, err
	}
	if userID != "" && userID != r.Order.UserID {
		return model.PaymentOrder{}, ErrNotFound
	}
	o := r.Order
	if userID != "" {
		o.Email = ""
	}
	if o.Status == "pending" && r.PayURLEncrypted != "" {
		o.PayURL, err = s.Vault.Decrypt(r.PayURLEncrypted)
		if err != nil {
			return model.PaymentOrder{}, err
		}
	}
	rows, err := s.DB.Query("SELECT id,at,kind,message,actual_amount FROM payment_activities WHERE order_id=? ORDER BY at,id LIMIT 100", id)
	if err != nil {
		return model.PaymentOrder{}, err
	}
	defer rows.Close()
	o.Activities = []model.PaymentActivity{}
	for rows.Next() {
		var a model.PaymentActivity
		var at int64
		var actual int64
		if err = rows.Scan(&a.ID, &at, &a.Kind, &a.Message, &actual); err != nil {
			return model.PaymentOrder{}, err
		}
		a.At = time.Unix(at, 0).UTC()
		if (a.Kind == "paid.notify" || a.Kind == "paid.query") && actual > 0 {
			a.ActualAmountCents = &actual
		}
		o.Activities = append(o.Activities, a)
	}
	return o, rows.Err()
}

func (s *Store) PaymentOrders(userID string, f model.PaymentOrderFilter) (model.PaymentOrderList, error) {
	out := model.PaymentOrderList{Items: []model.PaymentOrder{}}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 20
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	if f.Page > 1000000 {
		return out, errors.New("订单分页无效")
	}
	out.Page = f.Page
	out.PageSize = f.PageSize
	where := []string{"1=1"}
	args := []any{}
	if userID != "" {
		where = append(where, "o.user_id=?")
		args = append(args, userID)
	}
	if len(f.Search) > 120 {
		return out, errors.New("订单搜索内容过长")
	}
	if f.Search != "" {
		where = append(where, "(o.order_no LIKE ? OR o.template_name LIKE ? OR u.email LIKE ?)")
		v := "%" + strings.TrimSpace(f.Search) + "%"
		args = append(args, v, v, v)
	}
	if f.From != nil {
		where = append(where, "o.created_at>=?")
		args = append(args, f.From.Unix())
	}
	if f.To != nil {
		where = append(where, "o.created_at<?")
		args = append(args, f.To.Unix())
	}
	now := s.Now()
	if f.Status != "" && f.Status != "all" {
		switch f.Status {
		case "fulfilled":
			where = append(where, "o.payment_state='paid' AND o.fulfillment_state='fulfilled'")
		case "paid_pending":
			where = append(where, "o.payment_state='paid' AND o.fulfillment_state!='fulfilled'")
		case "closed":
			where = append(where, "o.payment_state='unpaid' AND o.closed_at>0")
		case "expired":
			where = append(where, "o.payment_state='unpaid' AND o.closed_at=0 AND o.expires_at<=?")
			args = append(args, now.Unix())
		case "creating", "pending", "unknown", "failed":
			state := f.Status
			if state == "pending" {
				state = "ready"
			}
			where = append(where, "o.payment_state='unpaid' AND o.closed_at=0 AND o.expires_at>? AND o.creation_state=?")
			args = append(args, now.Unix(), state)
		default:
			return out, errors.New("订单状态筛选无效")
		}
	}
	clause := " WHERE " + strings.Join(where, " AND ")
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM payment_orders o JOIN portal_users u ON u.id=o.user_id"+clause, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	pageArgs := append(append([]any{}, args...), f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.DB.Query(paymentOrderSelect+clause+" ORDER BY o.created_at DESC,o.id DESC LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanPaymentOrder(rows, now)
		if e != nil {
			return out, e
		}
		if userID != "" {
			r.Order.Email = ""
		}
		out.Items = append(out.Items, r.Order)
	}
	return out, rows.Err()
}

func (s *Store) ClosePaymentOrder(userID, id string, expected int64) (model.PaymentOrder, error) {
	err := retryDatabaseError(func() error {
		tx, e := s.DB.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		r, e := paymentRecord(tx, id, s.Now())
		if e != nil {
			return e
		}
		if userID != "" && r.Order.UserID != userID {
			return ErrNotFound
		}
		if r.ClosedAt != 0 {
			return nil
		}
		if r.Order.Revision != expected {
			return ErrConflict
		}
		if r.Order.PaymentState == "paid" {
			return errors.New("订单已付款，不能取消")
		}
		if _, e = tx.Exec("UPDATE payment_orders SET closed_at=?,revision=revision+1 WHERE id=?", s.Now().Unix(), id); e != nil {
			return e
		}
		if e = paymentActivity(tx, id, "closed", "用户关闭支付入口；已付款通知仍会核验处理", s.Now().Unix()); e != nil {
			return e
		}
		if e = expireNodeReservations(tx, s.Now()); e != nil {
			return e
		}
		return tx.Commit()
	})
	if err != nil {
		return model.PaymentOrder{}, err
	}
	return s.PaymentOrder(userID, id)
}

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
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

func paymentOrderNumber() (string, error) {
	var b [14]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "HQN" + hex.EncodeToString(b[:]), nil
}

// CreatePaymentOrder claims dispatch inside the serializable transaction. Only
// the transaction winner calls the provider, once, outside database locks.
// Retries with the same request ID return the existing record even after a
// response timeout; the provider's startOrder API has no documented idempotency.
func (s *Store) CreatePaymentOrder(ctx context.Context, userID string, in model.PaymentOrderInput, client *payment.Client) (model.PaymentOrder, error) {
	if !validRequestKey(in.RequestID) || in.TemplateID == "" || in.TemplateRevision < 1 || (in.RenewalSubscriptionID != "" && in.ExpectedSubscriptionRevision < 1) || (in.RenewalSubscriptionID == "" && in.ExpectedSubscriptionRevision != 0) {
		return model.PaymentOrder{}, errors.New("支付订单请求无效")
	}
	type prepared struct {
		id       string
		dispatch bool
		raced    bool
		request  payment.CreateRequest
	}
	p, err := retryDatabase(func() (prepared, error) {
		var out prepared
		tx, e := s.DB.Begin()
		if e != nil {
			return out, e
		}
		defer tx.Rollback()
		if e = lockAllocationUser(tx, userID); e != nil {
			return out, e
		}
		var oldTemplate, oldSubscription string
		var oldRevision, oldSubscriptionRevision int64
		e = tx.QueryRow("SELECT o.id,o.template_id,o.template_revision,COALESCE(r.subscription_id,''),COALESCE(r.expected_revision,0) FROM payment_orders o LEFT JOIN payment_renewals r ON r.order_id=o.id WHERE o.user_id=? AND o.request_key=?", userID, in.RequestID).Scan(&out.id, &oldTemplate, &oldRevision, &oldSubscription, &oldSubscriptionRevision)
		if e == nil {
			if oldTemplate != in.TemplateID || oldRevision != in.TemplateRevision || oldSubscription != in.RenewalSubscriptionID || oldSubscriptionRevision != in.ExpectedSubscriptionRevision {
				return out, ErrConflict
			}
			return out, nil
		}
		if e != sql.ErrNoRows {
			return out, e
		}
		var enabled bool
		var configVersion int64
		if e = tx.QueryRow("SELECT enabled,current_version FROM payment_settings WHERE id=1").Scan(&enabled, &configVersion); e != nil {
			return out, e
		}
		if !enabled || configVersion == 0 {
			return out, ErrPaymentDisabled
		}
		if e = tx.QueryRow("SELECT enabled FROM portal_users WHERE id=?", userID).Scan(&enabled); e != nil {
			return out, notFound(e)
		}
		if !enabled {
			return out, ErrEntitlement
		}
		template, e := subscriptionTemplate(tx, in.TemplateID)
		if e != nil {
			return out, e
		}
		if template.Revision != in.TemplateRevision {
			return out, ErrConflict
		}
		if !template.Enabled || !template.Listed || template.PriceCents == nil || (in.RenewalSubscriptionID == "" && len(template.NodeIDs) == 0) {
			return out, errors.New("该订阅暂不可购买")
		}
		if e = validSubscriptionTemplate(template); e != nil {
			return out, e
		}
		config, e := readPaymentConfiguration(tx, configVersion)
		if e != nil {
			return out, e
		}
		config, e = s.decryptPaymentConfiguration(config)
		if e != nil {
			return out, e
		}
		if e = payment.ValidateConfig(config.Config); e != nil {
			return out, ErrPaymentDisabled
		}
		var outstanding int
		if e = tx.QueryRow("SELECT COUNT(*) FROM payment_orders WHERE user_id=? AND payment_state='unpaid' AND closed_at=0 AND expires_at>?", userID, s.Now().Unix()).Scan(&outstanding); e != nil {
			return out, e
		}
		if outstanding >= 5 {
			return out, errors.New("待支付订单较多，请先处理已有订单")
		}
		no, e := paymentOrderNumber()
		if e != nil {
			return out, e
		}
		out.id = NewID()
		now := s.Now()
		expires := now.Add(time.Duration(config.Config.PayDurationMinutes) * time.Minute)
		if in.RenewalSubscriptionID != "" {
			subscription, err := validateRenewalOrder(tx, userID, in, now)
			if err != nil {
				return out, err
			}
			if expires.After(subscription.ExpiresAt) {
				expires = subscription.ExpiresAt
			}
		}
		result, e := tx.Exec("INSERT INTO payment_orders(id,order_no,user_id,template_id,request_key,template_revision,template_name,template_snapshot,amount,validity_days,config_version,provider_identity,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(user_id,request_key) DO NOTHING", out.id, no, userID, template.ID, in.RequestID, template.Revision, template.Name, jsonString(template), *template.PriceCents, template.ValidityDays, configVersion, config.Identity, now.Unix(), expires.Unix())
		if e != nil {
			return out, e
		}
		inserted, e := result.RowsAffected()
		if e != nil {
			return out, e
		}
		if inserted == 0 {
			// Only the explicit idempotency key can suppress this insert. Leave
			// this transaction's stale snapshot and resolve the committed winner
			// below; unrelated unique violations still propagate as errors.
			out.raced = true
			return out, nil
		}
		message := "订单已创建，节点已预留，正在获取支付链接"
		if in.RenewalSubscriptionID != "" {
			_, e = tx.Exec("INSERT INTO payment_renewals(order_id,subscription_id,expected_revision) VALUES(?,?,?)", out.id, in.RenewalSubscriptionID, in.ExpectedSubscriptionRevision)
			message = "续费订单已创建，保留原订阅与节点；付款后延期，下期开始时重置流量"
		} else {
			_, e = reserveOrderNodes(tx, out.id, userID, template, template.NodesPerSubscription, now)
		}
		if e != nil {
			return out, e
		}
		if e = paymentActivity(tx, out.id, "created", message, now.Unix()); e != nil {
			return out, e
		}
		out.dispatch = true
		out.request = payment.CreateRequest{Config: config.Config, OrderID: out.id, OrderNo: no, AmountCents: *template.PriceCents, Subject: template.Name, NotifyURL: s.BaseURL + "/api/v1/payments/fm/notify"}
		return out, tx.Commit()
	})
	if err != nil {
		return model.PaymentOrder{}, err
	}
	if p.raced {
		var template, subscription string
		var revision, subscriptionRevision int64
		if err = s.DB.QueryRow("SELECT o.id,o.template_id,o.template_revision,COALESCE(r.subscription_id,''),COALESCE(r.expected_revision,0) FROM payment_orders o LEFT JOIN payment_renewals r ON r.order_id=o.id WHERE o.user_id=? AND o.request_key=?", userID, in.RequestID).Scan(&p.id, &template, &revision, &subscription, &subscriptionRevision); err != nil {
			return model.PaymentOrder{}, err
		}
		if template != in.TemplateID || revision != in.TemplateRevision || subscription != in.RenewalSubscriptionID || subscriptionRevision != in.ExpectedSubscriptionRevision {
			return model.PaymentOrder{}, ErrConflict
		}
	}
	if p.dispatch {
		created, createErr := client.Create(ctx, p.request)
		if err = s.finishPaymentCreation(p.id, created, createErr); err != nil {
			return model.PaymentOrder{}, err
		}
	}
	return s.PaymentOrder(userID, p.id)
}

func (s *Store) finishPaymentCreation(id string, created payment.CreatedOrder, createErr error) error {
	return retryDatabaseError(func() error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		r, err := paymentRecord(tx, id, s.Now())
		if err != nil {
			return err
		}
		if r.Order.CreationState != "creating" && r.Order.CreationState != "unknown" {
			return nil
		}
		state, message, encrypted, platform := "ready", "", r.PayURLEncrypted, r.Order.PlatformOrderNo
		if createErr != nil {
			state = "unknown"
			message = "支付创建结果待确认，请勿重复付款"
			var known *payment.ProviderError
			if errors.As(createErr, &known) && !known.Uncertain {
				state = "failed"
				message = known.Error()
			}
		} else {
			var collision int
			if err = tx.QueryRow("SELECT COUNT(*) FROM payment_orders WHERE provider_identity=? AND platform_order_no=? AND id!=?", r.ProviderIdentity, created.PlatformOrderNo, id).Scan(&collision); err != nil {
				return err
			}
			if collision != 0 || platform != "" && platform != created.PlatformOrderNo {
				state = "unknown"
				message = "支付平台订单标识冲突，请联系管理员"
				encrypted = ""
			} else {
				platform = created.PlatformOrderNo
				encrypted, err = s.Vault.Encrypt(created.PayURL)
				if err != nil {
					return err
				}
			}
		}
		if _, err = tx.Exec("UPDATE payment_orders SET creation_state=?,pay_url=?,platform_order_no=?,last_error=CASE WHEN payment_state='unpaid' THEN ? ELSE last_error END,revision=revision+1 WHERE id=?", state, encrypted, platform, message, id); err != nil {
			return err
		}
		activity := message
		if activity == "" {
			activity = "支付链接已生成"
		}
		if err = paymentActivity(tx, id, "creation."+state, activity, s.Now().Unix()); err != nil {
			return err
		}
		if err = expireNodeReservations(tx, s.Now()); err != nil {
			return err
		}
		return tx.Commit()
	})
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

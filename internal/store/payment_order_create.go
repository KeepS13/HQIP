package store

// Payment creation owns inventory reservation and provider dispatch. The
// provider call stays outside the transaction; uncertain outcomes retain stock.
import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
)

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
		// Hidden plans remain renewable by their owners; only new purchases
		// require a shop listing. validateRenewalOrder checks ownership below.
		if !template.Enabled || template.PriceCents == nil || (in.RenewalSubscriptionID == "" && !template.Listed) {
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
			if _, err = renewalReplacementNodes(tx, userID, subscription.ID, template, now); err != nil {
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
			message = "续费订单已创建，付款核验时确认原节点并延长订阅，下期开始时重置流量"
		} else if len(template.NodeIDs) == 0 {
			message = "定制订阅订单已创建，付款后提供订阅链接；管理员分配节点并开通后开始计时"
		} else {
			_, e = reserveOrderNodes(tx, out.id, userID, template, subscriptionNodeTarget(template), now)
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

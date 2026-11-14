package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
)

// ProcessPaymentNotify acknowledges only durable receipt. Fulfillment is a
// separate short transaction performed by the worker; no external HTTP occurs
// while handling the provider's three-second callback deadline.
func (s *Store) ProcessPaymentNotify(values url.Values) error {
	if len(values) > 24 || len(values.Get("orderNo")) > 32 {
		return ErrPaymentNotify
	}
	return retryDatabaseError(func() error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		r, err := scanPaymentOrder(tx.QueryRow(paymentOrderSelect+"WHERE o.order_no=?", values.Get("orderNo")), s.Now())
		if errors.Is(err, ErrNotFound) {
			return ErrPaymentNotify
		}
		if err != nil {
			return err
		}
		original, err := readPaymentConfiguration(tx, r.ConfigVersion)
		if err != nil {
			return ErrPaymentNotify
		}
		if values.Get("merchantNum") != original.Config.MerchantNum {
			return ErrPaymentNotify
		}
		rows, err := tx.Query("SELECT version,secret FROM payment_config_versions WHERE identity=? AND version>=? AND revoked=0 AND secret!='' ORDER BY version DESC", r.ProviderIdentity, r.ConfigVersion)
		if err != nil {
			return err
		}
		type key struct {
			version    int64
			ciphertext string
		}
		keys := []key{}
		for rows.Next() {
			var k key
			if err = rows.Scan(&k.version, &k.ciphertext); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, k)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var proof payment.NotifyProof
		var signedVersion int64
		for _, k := range keys {
			secret, e := s.Vault.Decrypt(k.ciphertext)
			if e != nil {
				return e
			}
			candidate, e := payment.VerifyNotify(values, secret)
			if e == nil {
				proof = candidate
				signedVersion = k.version
				break
			}
		}
		if signedVersion == 0 || proof.OrderNo != r.Order.OrderNo || proof.AmountCents != r.Order.AmountCents {
			return ErrPaymentNotify
		}
		if r.Order.PlatformOrderNo != "" && proof.PlatformOrderNo != r.Order.PlatformOrderNo {
			return ErrPaymentNotify
		}
		if err = recordVerifiedPayment(tx, r, proof, signedVersion, s.Now(), false); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func recordVerifiedPayment(tx *sql.Tx, r paymentOrderRecord, p payment.NotifyProof, signatureVersion int64, now time.Time, queried bool) error {
	if r.Order.PaymentState == "paid" {
		_, err := tx.Exec("UPDATE payment_orders SET last_notify_at=? WHERE id=?", now.Unix(), r.Order.ID)
		return err
	}
	if err := lockAllocationUser(tx, r.Order.UserID); err != nil {
		return err
	}
	// Release an expired/cancelled unpaid hold before marking a late notice paid.
	if err := expireNodeReservations(tx, now); err != nil {
		return err
	}
	// The callback's platformOrderNo is not signed. It is recorded as evidence
	// but cannot acquire the trusted global platform-ID uniqueness constraint.
	if _, err := tx.Exec("UPDATE payment_orders SET payment_state='paid',paid_at=?,last_notify_at=?,fulfillment_state='pending',last_error='',revision=revision+1 WHERE id=? AND payment_state='unpaid'", now.Unix(), now.Unix(), r.Order.ID); err != nil {
		return err
	}
	kind, message := "paid.notify", "支付通知已核验，等待开通订阅"
	if queried {
		kind = "paid.query"
		message = "支付平台查单确认已付款，等待开通订阅"
	}
	_, err := tx.Exec("INSERT INTO payment_activities(id,order_id,at,kind,message,signature_version,reported_platform_order_no,actual_amount,reported_paid_at) VALUES(?,?,?,?,?,?,?,?,?)", NewID(), r.Order.ID, now.Unix(), kind, message, signatureVersion, p.PlatformOrderNo, p.ActualAmountCents, p.ReportedPaidAt)
	return err
}

func (s *Store) fulfillPaymentOrder(id string) (bool, error) {
	return retryDatabase(func() (bool, error) {
		tx, err := s.DB.Begin()
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		r, err := paymentRecord(tx, id, s.Now())
		if err != nil {
			return false, err
		}
		if r.Order.PaymentState != "paid" || r.Order.FulfillmentState == "fulfilled" {
			return false, nil
		}
		if err = lockAllocationUser(tx, r.Order.UserID); err != nil {
			return false, err
		}
		now := s.Now()
		var enabled bool
		if err = tx.QueryRow("SELECT enabled FROM portal_users WHERE id=?", r.Order.UserID).Scan(&enabled); err != nil {
			return false, err
		}
		template, err := subscriptionTemplate(tx, r.Order.TemplateID)
		if err != nil {
			return false, err
		}
		if !enabled || !template.Enabled || len(template.NodeIDs) == 0 {
			message := "用户已停用，已付款订单等待管理员处理"
			if !template.Enabled {
				message = "订阅模板已停用，已付款订单等待管理员处理"
			} else if len(template.NodeIDs) == 0 {
				message = "订阅模板暂无可用节点，已付款订单等待管理员处理"
			}
			if r.Order.FulfillmentState != "blocked" || r.Order.LastError != message {
				if _, err = tx.Exec("UPDATE payment_orders SET fulfillment_state='blocked',last_error=?,revision=revision+1 WHERE id=?", message, id); err != nil {
					return false, err
				}
				if err = paymentActivity(tx, id, "fulfillment.blocked", message, now.Unix()); err != nil {
					return false, err
				}
			}
			return false, tx.Commit()
		}
		// Serialize fulfillment on the order before creating its unique grant.
		// A competing PostgreSQL transaction must retry its stale snapshot here,
		// not discover the race as an unrelated grant-key unique violation later.
		claimed, err := tx.Exec("UPDATE payment_orders SET revision=revision WHERE id=? AND revision=? AND payment_state='paid' AND fulfillment_state!='fulfilled'", id, r.Order.Revision)
		if err != nil {
			return false, err
		}
		changed, err := claimed.RowsAffected()
		if err != nil {
			return false, err
		}
		if changed != 1 {
			return false, nil
		}
		e, err := grantSubscriptionWithValidity(tx, r.Order.UserID, r.Order.TemplateID, "payment:"+id, -1, template.Revision, now, r.Order.ValidityDays)
		if errors.Is(err, ErrSubscriptionStock) {
			message := "节点库存不足，已付款订单等待管理员处理"
			if _, err = tx.Exec("UPDATE payment_orders SET fulfillment_state='blocked',last_error=?,revision=revision+1 WHERE id=?", message, id); err != nil {
				return false, err
			}
			if err = paymentActivity(tx, id, "fulfillment.blocked", message, now.Unix()); err != nil {
				return false, err
			}
			return false, tx.Commit()
		}
		if err != nil {
			return false, err
		}
		if _, err = tx.Exec("UPDATE payment_orders SET fulfillment_state='fulfilled',fulfilled_at=?,subscription_id=?,last_error='',revision=revision+1 WHERE id=?", now.Unix(), e.ID, id); err != nil {
			return false, err
		}
		if err = paymentActivity(tx, id, "fulfilled", "订阅已开通，有效期从本次开通时开始计算", now.Unix()); err != nil {
			return false, err
		}
		if _, err = tx.Exec("UPDATE payment_node_reservations SET released=1 WHERE order_id=?", id); err != nil {
			return false, err
		}
		if _, err = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'payment.fulfilled',?)", now.Unix(), id+":"+e.ID); err != nil {
			return false, err
		}
		return true, tx.Commit()
	})
}

// FulfillPaymentOrders is independent of the checkout enabled switch: disabling
// new sales must never lose a payment already made. Blocked orders require an
// explicit administrator retry so enabling a user/template is not a hidden sale.
func (s *Store) FulfillPaymentOrders(limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	if _, err := s.DB.Exec("UPDATE payment_orders SET creation_state='unknown',last_error=CASE WHEN payment_state='unpaid' THEN '支付创建结果待确认，请勿重复付款' ELSE last_error END,revision=revision+1 WHERE creation_state='creating' AND created_at<?", s.Now().Add(-time.Minute).Unix()); err != nil {
		return 0, err
	}
	rows, err := s.DB.Query("SELECT id FROM payment_orders WHERE payment_state='paid' AND fulfillment_state='pending' ORDER BY paid_at,id LIMIT ?", limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	done, failed := 0, 0
	for _, id := range ids {
		ok, e := s.fulfillPaymentOrder(id)
		if e != nil {
			failed++
			// A malformed historical record or one failed transaction must not
			// starve every later paid order. Store a safe, actionable failure;
			// never persist raw SQL/provider errors with credentials or payloads.
			_ = s.recordPaymentFulfillmentFailure(id)
			continue
		}
		if ok {
			done++
		}
	}
	if failed != 0 {
		return done, fmt.Errorf("%d 个已付款订单暂未开通，请在订单管理中重试", failed)
	}
	return done, nil
}

func (s *Store) recordPaymentFulfillmentFailure(id string) error {
	return retryDatabaseError(func() error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.Exec("UPDATE payment_orders SET fulfillment_state='blocked',last_error='订阅开通暂未完成，请管理员重试',revision=revision+1 WHERE id=? AND payment_state='paid' AND fulfillment_state='pending'", id)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return nil
		}
		if err = paymentActivity(tx, id, "fulfillment.failed", "订阅开通暂未完成，请管理员重试", s.Now().Unix()); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *Store) RetryPaymentFulfillment(id string, expected int64, actor string) (model.PaymentOrder, error) {
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
		if r.Order.FulfillmentState == "fulfilled" {
			return nil
		}
		if r.Order.Revision != expected {
			return ErrConflict
		}
		if r.Order.PaymentState != "paid" {
			return errors.New("未确认付款，不能补开订阅")
		}
		if _, e = tx.Exec("UPDATE payment_orders SET fulfillment_state='pending',revision=revision+1 WHERE id=?", id); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'payment.fulfillment_retry',?)", s.Now().Unix(), actor+":"+id); e != nil {
			return e
		}
		return tx.Commit()
	})
	if err != nil {
		return model.PaymentOrder{}, err
	}
	if _, err = s.fulfillPaymentOrder(id); err != nil {
		return model.PaymentOrder{}, err
	}
	return s.PaymentOrder("", id)
}

func (s *Store) ReconcilePaymentOrder(ctx context.Context, id string, expected int64, actor string, client *payment.Client) (model.PaymentOrder, error) {
	type request struct {
		config paymentConfiguration
		order  paymentOrderRecord
		skip   bool
	}
	in, err := retryDatabase(func() (request, error) {
		var out request
		tx, e := s.DB.Begin()
		if e != nil {
			return out, e
		}
		defer tx.Rollback()
		r, e := paymentRecord(tx, id, s.Now())
		if e != nil {
			return out, e
		}
		out.order = r
		if r.Order.PaymentState == "paid" {
			out.skip = true
			return out, nil
		}
		if r.Order.Revision != expected {
			return out, ErrConflict
		}
		var current, last int64
		if e = tx.QueryRow("SELECT current_version,last_query_at FROM payment_settings WHERE id=1").Scan(&current, &last); e != nil {
			return out, e
		}
		c, e := readPaymentConfiguration(tx, current)
		if e != nil {
			return out, e
		}
		if !c.QueryEnabled || c.Identity != r.ProviderIdentity {
			return out, ErrPaymentQueryDisabled
		}
		if s.Now().Unix()-last < 3 || s.Now().Unix()-r.LastReconcileAt < 30 {
			return out, errors.New("支付查询过于频繁，请稍后重试")
		}
		c, e = s.decryptPaymentConfiguration(c)
		if e != nil {
			return out, e
		}
		out.config = c
		if _, e = tx.Exec("UPDATE payment_settings SET last_query_at=? WHERE id=1", s.Now().Unix()); e != nil {
			return out, e
		}
		if _, e = tx.Exec("UPDATE payment_orders SET last_reconcile_at=? WHERE id=?", s.Now().Unix(), id); e != nil {
			return out, e
		}
		return out, tx.Commit()
	})
	if err != nil {
		return model.PaymentOrder{}, err
	}
	if in.skip {
		return s.PaymentOrder("", id)
	}
	result, queryErr := client.Query(ctx, in.config.Config, in.order.Order.OrderNo)
	if queryErr != nil {
		return model.PaymentOrder{}, queryErr
	}
	if result.MerchantNum != in.config.Config.MerchantNum || result.OrderNo != in.order.Order.OrderNo || result.AmountCents != in.order.Order.AmountCents {
		return model.PaymentOrder{}, ErrPaymentNotify
	}
	err = retryDatabaseError(func() error {
		tx, e := s.DB.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		r, e := paymentRecord(tx, id, s.Now())
		if e != nil {
			return e
		}
		if r.Order.PlatformOrderNo != "" && r.Order.PlatformOrderNo != result.PlatformOrderNo {
			return ErrPaymentNotify
		}
		if r.Order.PlatformOrderNo == "" {
			var used int
			if e = tx.QueryRow("SELECT COUNT(*) FROM payment_orders WHERE provider_identity=? AND platform_order_no=? AND id!=?", r.ProviderIdentity, result.PlatformOrderNo, id).Scan(&used); e != nil {
				return e
			}
			if used != 0 {
				return ErrPaymentNotify
			}
			if _, e = tx.Exec("UPDATE payment_orders SET platform_order_no=? WHERE id=?", result.PlatformOrderNo, id); e != nil {
				return e
			}
		}
		if result.State == "4" {
			proof := payment.NotifyProof{MerchantNum: result.MerchantNum, OrderNo: result.OrderNo, PlatformOrderNo: result.PlatformOrderNo, PayType: result.PayType, AmountCents: result.AmountCents, ActualAmountCents: result.ActualAmountCents, ReportedPaidAt: result.ReportedPaidAt}
			if e = recordVerifiedPayment(tx, r, proof, 0, s.Now(), true); e != nil {
				return e
			}
		} else if result.State == "7" && r.Order.PaymentState == "unpaid" {
			if _, e = tx.Exec("UPDATE payment_orders SET closed_at=?,pay_url='',revision=revision+1 WHERE id=? AND payment_state='unpaid'", s.Now().Unix(), id); e != nil {
				return e
			}
		} else if result.State != "2" && result.State != "7" {
			return errors.New("支付平台返回未知状态")
		}
		if e = paymentActivity(tx, id, "reconciled", "管理员已向支付平台核对订单状态", s.Now().Unix()); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'payment.reconciled',?)", s.Now().Unix(), actor+":"+id); e != nil {
			return e
		}
		return tx.Commit()
	})
	if err != nil {
		return model.PaymentOrder{}, err
	}
	return s.PaymentOrder("", id)
}

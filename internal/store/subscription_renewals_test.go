package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
	"github.com/apernet/hysteria/core/v2/billing"
)

func renewalFixture(t *testing.T) (*Store, *time.Time, model.PortalUser, model.SubscriptionTemplate, model.UserSubscription, string) {
	t.Helper()
	s, now, user, template, _ := paymentFixture(t)
	quota := int64(64 << 20)
	template.QuotaBytes = &quota
	var err error
	template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	order := makePaymentOrder(t, s, user, template, "initial-purchase")
	if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FulfillPaymentOrders(25); err != nil {
		t.Fatal(err)
	}
	order, err = s.PaymentOrder(user.ID, order.ID)
	if err != nil || order.SubscriptionID == "" {
		t.Fatal(order, err)
	}
	sub, err := s.UserSubscription(user.ID, order.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	password := subscriptionPassword(t, s, user.ID, sub.ID, sub.NodeIDs[0])
	sub, err = s.UserSubscription(user.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, now, user, template, *sub, password
}

func renewalInput(template model.SubscriptionTemplate, sub model.UserSubscription, key string) model.PaymentOrderInput {
	return model.PaymentOrderInput{RequestID: key, TemplateID: template.ID, TemplateRevision: template.Revision, RenewalSubscriptionID: sub.ID, ExpectedSubscriptionRevision: sub.Revision}
}

func paidRenewal(t *testing.T, s *Store, template model.SubscriptionTemplate, sub model.UserSubscription, key string) model.PaymentOrder {
	t.Helper()
	order, err := s.CreatePaymentOrder(context.Background(), sub.UserID, renewalInput(template, sub, key), paymentTestClient())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	order, err = s.PaymentOrder(sub.UserID, order.ID)
	if err != nil || order.FulfillmentState != "fulfilled" || order.SubscriptionID != sub.ID || order.OrderType != "renewal" {
		t.Fatal(order, err)
	}
	return order
}

func TestRenewalPreservesIdentityDefersResetAndIsolatesLateUsage(t *testing.T) {
	s, now, user, template, sub, password := renewalFixture(t)
	firstEnd := sub.ExpiresAt
	*now = firstEnd.Add(-30 * time.Second)
	link, err := s.PortalSubscription(user.ID, sub.ID, "get")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(sub.NodeIDs[0], strings.Repeat("a", 32), password)
	if err != nil || !lease.ExpiresAt.Equal(firstEnd) {
		t.Fatal(lease, err)
	}
	lease, err = s.RenewLease(lease.NodeID, lease.ID, model.LeaseUsage{UploadBytes: 1000, DownloadBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.UserSubscription(user.ID, sub.ID)
	first := paidRenewal(t, s, template, *before, "first-renewal")
	after, _ := s.UserSubscription(user.ID, sub.ID)
	if after.UsedBytes != before.UsedBytes || after.ReservedBytes != before.ReservedBytes || !after.CurrentPeriodEndsAt.Equal(firstEnd) || after.NextResetAt == nil || !after.NextResetAt.Equal(firstEnd) || !reflect.DeepEqual(after.NodeIDs, sub.NodeIDs) {
		t.Fatal("renewal changed current entitlement", after)
	}
	if !first.PeriodStartsAt.Equal(firstEnd) || !after.ExpiresAt.Equal(firstEnd.Add(24*time.Hour)) {
		t.Fatal(first, after)
	}
	paidRenewal(t, s, template, *after, "second-renewal")
	after, _ = s.UserSubscription(user.ID, sub.ID)
	if len(after.Periods) != 3 || !after.ExpiresAt.Equal(firstEnd.Add(48*time.Hour)) {
		t.Fatal(after)
	}
	var reservations int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM payment_node_reservations r JOIN payment_renewals n ON n.order_id=r.order_id").Scan(&reservations); err != nil || reservations != 0 {
		t.Fatal("renewal reserved extra stock", reservations, err)
	}
	lease, err = s.RenewLease(lease.NodeID, lease.ID, model.LeaseUsage{UploadBytes: 1000, DownloadBytes: 2000})
	if err != nil || !lease.ExpiresAt.Equal(firstEnd) {
		t.Fatal("lease crossed period boundary", lease, err)
	}
	*now = firstEnd
	current, _ := s.UserSubscription(user.ID, sub.ID)
	if current.UsedBytes != 0 || current.ReservedBytes != 0 || current.Status != "active" || current.CurrentPeriodID == sub.ID {
		t.Fatal("new period did not reset", current)
	}
	if _, err = s.RenewLease(lease.NodeID, lease.ID, model.LeaseUsage{UploadBytes: 1100, DownloadBytes: 2200, Closed: true}); err != nil {
		t.Fatal(err)
	}
	current, _ = s.UserSubscription(user.ID, sub.ID)
	if current.UsedBytes != 0 || current.ReservedBytes != 0 || current.Periods[0].UploadBytes+current.Periods[0].DownloadBytes != 3300 {
		t.Fatal("late usage leaked into new period", current)
	}
	newLease, err := s.AcquireLease(sub.NodeIDs[0], strings.Repeat("b", 32), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewLease(newLease.NodeID, newLease.ID, model.LeaseUsage{DownloadBytes: 4000, Closed: true}); err != nil {
		t.Fatal(err)
	}
	current, _ = s.UserSubscription(user.ID, sub.ID)
	if current.UsedBytes != 4000 || current.ReservedBytes != 0 {
		t.Fatal(current)
	}
	newLink, err := s.PortalSubscription(user.ID, sub.ID, "get")
	if err != nil || newLink != link {
		t.Fatal("renewal changed import link", err)
	}
	*now = firstEnd.Add(24 * time.Hour)
	other, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.Now = s.Now
	current, err = other.UserSubscription(user.ID, sub.ID)
	if err != nil || current.UsedBytes != 0 || current.ReservedBytes != 0 || current.CurrentPeriodID != current.Periods[2].ID {
		t.Fatal("restart lost future period", current, err)
	}
	var lifetime int64
	if err = s.DB.QueryRow("SELECT upload+download FROM portal_usage WHERE subscription_id=?", sub.ID).Scan(&lifetime); err != nil || lifetime != 7300 {
		t.Fatal("lifetime ledger changed", lifetime, err)
	}
	*now = current.ExpiresAt
	current, _ = s.UserSubscription(user.ID, sub.ID)
	if current.Status != "expired" || current.Renewable {
		t.Fatal("unpaid extra period appeared", current)
	}
}

func TestRenewalConcurrentReceiptsAppendExactlyOnce(t *testing.T) {
	s, _, user, template, sub, _ := renewalFixture(t)
	other := paymentConcurrentStore(t, s)
	stores := []*Store{s, other}
	orders := make([]model.PaymentOrder, 3)
	for i := range orders {
		var err error
		orders[i], err = s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(template, sub, fmt.Sprintf("concurrent-%d", i)), paymentTestClient())
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := stores[i%2].ProcessPaymentNotify(paymentNotice(orders[i%3], "test-payment-secret-v1")); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	after, err := s.UserSubscription(user.ID, sub.ID)
	if err != nil || len(after.Periods) != 4 || !after.ExpiresAt.Equal(sub.ExpiresAt.Add(72*time.Hour)) {
		t.Fatal(after, err)
	}
	for _, order := range orders {
		o, err := s.PaymentOrder(user.ID, order.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RetryPaymentFulfillment(o.ID, o.Revision, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	again, _ := s.UserSubscription(user.ID, sub.ID)
	if !again.ExpiresAt.Equal(after.ExpiresAt) || len(again.Periods) != 4 {
		t.Fatal("retry duplicated period", again)
	}
}

func TestRenewalEligibilityAndIdempotency(t *testing.T) {
	for _, mode := range []string{"foreign", "admin-grant", "expired", "revoked", "disabled-user", "disabled-template", "unlisted", "unpriced", "exhausted", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, now, user, template, sub, _ := renewalFixture(t)
			in := renewalInput(template, sub, "eligibility-request")
			switch mode {
			case "foreign":
				user, _ = s.RegisterPortal("foreign-renewal@example.com", "password-123")
			case "admin-grant":
				in.RenewalSubscriptionID = user.Subscriptions[0].ID
				in.ExpectedSubscriptionRevision = user.Subscriptions[0].Revision
			case "expired":
				*now = sub.ExpiresAt
			case "revoked":
				audienceExec(t, s, "UPDATE portal_subscriptions SET revoked=1 WHERE id=?", sub.ID)
			case "disabled-user":
				audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", user.ID)
			case "disabled-template":
				template.Enabled = false
			case "unlisted":
				template.Listed = false
			case "unpriced":
				template.PriceCents = nil
			case "exhausted":
				audienceExec(t, s, "UPDATE portal_usage SET upload=? WHERE subscription_id=?", *template.QuotaBytes, sub.ID)
			case "stale":
				in.ExpectedSubscriptionRevision++
			}
			if mode == "disabled-template" || mode == "unlisted" || mode == "unpriced" {
				var err error
				template, err = s.SaveSubscriptionTemplate(template.ID, template, template.Revision)
				if err != nil {
					t.Fatal(err)
				}
				in.TemplateRevision = template.Revision
			}
			var calls atomic.Int32
			client := paymentTestClient()
			inner := client.Transport
			client.Transport = paymentTestTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return inner.RoundTrip(r) })
			o, err := s.CreatePaymentOrder(context.Background(), user.ID, in, client)
			if mode == "exhausted" {
				if err != nil {
					t.Fatal(err)
				}
				if err = s.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); err != nil {
					t.Fatal(err)
				}
				after, _ := s.UserSubscription(user.ID, sub.ID)
				if after.Status != "exhausted" {
					t.Fatal("borrowed next period quota", after)
				}
				return
			}
			if err == nil || calls.Load() != 0 {
				t.Fatal("ineligible renewal contacted provider", mode, err, calls.Load())
			}
		})
	}
	s, _, user, template, sub, _ := renewalFixture(t)
	in := renewalInput(template, sub, "timeout-renewal")
	var calls atomic.Int32
	client := &payment.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, context.DeadlineExceeded })}
	first, err := s.CreatePaymentOrder(context.Background(), user.ID, in, client)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreatePaymentOrder(context.Background(), user.ID, in, client)
	if err != nil || first.ID != second.ID || calls.Load() != 1 {
		t.Fatal("uncertain renewal redispatched", err, calls.Load())
	}
	wrong := in
	wrong.RenewalSubscriptionID = ""
	wrong.ExpectedSubscriptionRevision = 0
	if _, err = s.CreatePaymentOrder(context.Background(), user.ID, wrong, client); !errors.Is(err, ErrConflict) {
		t.Fatal("key reused for purchase", err)
	}
	if err = s.ProcessPaymentNotify(paymentNotice(first, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	after, _ := s.UserSubscription(user.ID, sub.ID)
	if len(after.Periods) != 2 {
		t.Fatal(after)
	}
}

func TestRenewalLateReceiptAndExpiryAdjustment(t *testing.T) {
	s, now, user, template, sub, _ := renewalFixture(t)
	*now = sub.ExpiresAt.Add(-5 * time.Second)
	o, err := s.CreatePaymentOrder(context.Background(), user.ID, renewalInput(template, sub, "late-renewal"), paymentTestClient())
	if err != nil || !o.ExpiresAt.Equal(sub.ExpiresAt) {
		t.Fatal(o, err)
	}
	*now = sub.ExpiresAt
	if err = s.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); err != nil {
		t.Fatal(err)
	}
	o, err = s.PaymentOrder(user.ID, o.ID)
	if err != nil || o.PaymentState != "paid" || o.FulfillmentState != "blocked" {
		t.Fatal(o, err)
	}
	if _, err = s.RetryPaymentFulfillment(o.ID, o.Revision, "admin"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.UserSubscription(user.ID, sub.ID)
	if len(after.Periods) != 1 || !after.ExpiresAt.Equal(sub.ExpiresAt) {
		t.Fatal("late payment resurrected expired subscription", after)
	}
	// A second fixture exercises adjustment of the final prepaid period only.
	s, _, user, template, sub, _ = renewalFixture(t)
	paidRenewal(t, s, template, sub, "before-expiry-edit")
	after, _ = s.UserSubscription(user.ID, sub.ID)
	if _, err = s.UpdateSubscriptionExpiry(user.ID, sub.ID, sub.ExpiresAt, after.Revision); !errors.Is(err, ErrSubscriptionPeriodExpiry) {
		t.Fatal("cut off prepaid cycle", err)
	}
	updated, err := s.UpdateSubscriptionExpiry(user.ID, sub.ID, after.ExpiresAt.Add(time.Hour), after.Revision)
	if err != nil || !updated.CurrentPeriodEndsAt.Equal(sub.ExpiresAt) || !updated.Periods[1].EndsAt.Equal(updated.ExpiresAt) {
		t.Fatal(updated, err)
	}
}

func TestRenewalBillingRemainderAndOfflineLateSettlement(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(fmt.Sprint(offline), func(t *testing.T) {
			s, now, user, template, sub, password := renewalFixture(t)
			enableBilling(t, s, 150, 150)
			if offline {
				if err := s.SetOfflineAdmissionEnabled(sub.NodeIDs[0], true); err != nil {
					t.Fatal(err)
				}
			}
			*now = sub.ExpiresAt.Add(-time.Minute)
			old, err := s.AcquireProtocolLease(sub.NodeIDs[0], strings.Repeat("c", 32), password, "hysteria2", offline)
			if err != nil || !old.ExpiresAt.Equal(sub.ExpiresAt) {
				t.Fatal(old, err)
			}
			paidRenewal(t, s, template, sub, "billing-renewal")
			*now = sub.ExpiresAt
			fresh, err := s.AcquireProtocolLease(sub.NodeIDs[0], strings.Repeat("d", 32), password, "hysteria2", false)
			if err != nil {
				t.Fatal(err)
			}
			segment := func(l model.ConnectionLease) []billing.Segment {
				return []billing.Segment{{Version: l.Billing.Version, Upload: 1, AppliedAt: l.CreatedAt}}
			}
			if _, err = s.RenewLease(fresh.NodeID, fresh.ID, model.LeaseUsage{UploadBytes: 1, Closed: true, Billing: segment(fresh)}); err != nil {
				t.Fatal(err)
			}
			if offline {
				_, err = s.ReportOfflineGrant(old.NodeID, old.ID, model.OfflineUsage{Version: 1, UploadBytes: 1, Closed: true, Billing: segment(old)})
			} else {
				_, err = s.RenewLease(old.NodeID, old.ID, model.LeaseUsage{UploadBytes: 1, Closed: true, Billing: segment(old)})
			}
			if err != nil {
				t.Fatal(err)
			}
			after, _ := s.UserSubscription(user.ID, sub.ID)
			if after.UsedBytes != 1 || after.ReservedBytes != 0 {
				t.Fatal("old rounding or reservation leaked", after)
			}
			for _, period := range after.Periods {
				var remainder int64
				if err = s.DB.QueryRow("SELECT remainder FROM subscription_periods WHERE id=?", period.ID).Scan(&remainder); err != nil || remainder != 50 {
					t.Fatal(period, remainder, err)
				}
			}
		})
	}
}

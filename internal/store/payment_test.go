package store

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
)

type paymentTestTransport func(*http.Request) (*http.Response, error)

func (f paymentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func paymentTestResponse(id string) *http.Response {
	body, _ := json.Marshal(map[string]any{"success": true, "code": 200, "data": map[string]string{"id": id, "payUrl": "https://pay.example.com/checkout/" + id}})
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
func paymentTestClient() *payment.Client {
	return &payment.Client{ReturnOrigin: "https://user.example.com", Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		v, _ := url.ParseQuery(string(body))
		return paymentTestResponse("p-" + v.Get("orderNo")), nil
	})}
}

func paymentFixture(t *testing.T) (*Store, *time.Time, model.PortalUser, model.SubscriptionTemplate, model.PaymentSettings) {
	t.Helper()
	s, now, u, p, _, _ := portalFixture(t, 2, nil)
	if e := EnsurePaymentSchema(s.DB); e != nil {
		t.Fatal(e)
	}
	price := int64(1200)
	p.PriceCents = &price
	p, e := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	secret := "test-payment-secret-v1"
	setting, e := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", Secret: &secret, PayType: "aloop", PayDurationMinutes: 10}, "admin")
	if e != nil {
		t.Fatal(e)
	}
	return s, now, u, p, setting
}
func makePaymentOrder(t *testing.T, s *Store, u model.PortalUser, p model.SubscriptionTemplate, key string) model.PaymentOrder {
	t.Helper()
	o, e := s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: key, TemplateID: p.ID, TemplateRevision: p.Revision}, paymentTestClient())
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func paymentNotice(o model.PaymentOrder, secret string) url.Values {
	amount := payment.FormatAmount(o.AmountCents)
	h := md5.Sum([]byte("1merchant1" + o.OrderNo + amount + secret))
	platform := o.PlatformOrderNo
	if platform == "" {
		platform = "p-" + o.OrderNo
	}
	return url.Values{"merchantNum": {"merchant1"}, "orderNo": {o.OrderNo}, "amount": {amount}, "state": {"1"}, "type": {"alipay"}, "platformOrderNo": {platform}, "actualPayAmount": {amount}, "payTime": {"2020-01-01 00:00:00"}, "sign": {hex.EncodeToString(h[:])}}
}

func TestPaymentSettingsEncryptMaskCASAndDoNotReuseAcrossMerchantIdentities(t *testing.T) {
	s, _, _, _, config := paymentFixture(t)
	raw, _ := json.Marshal(config)
	if strings.Contains(string(raw), "test-payment-secret") || !config.SecretConfigured || !config.Enabled {
		t.Fatal("settings leaked a secret")
	}
	var cipher string
	if e := s.DB.QueryRow("SELECT secret FROM payment_config_versions WHERE version=1").Scan(&cipher); e != nil || cipher == "test-payment-secret-v1" {
		t.Fatal("secret not encrypted", e)
	}
	in := model.PaymentSettingsInput{Enabled: true, APIBaseURL: config.APIBaseURL, MerchantNum: config.MerchantNum, PayType: config.PayType, PayDurationMinutes: config.PayDurationMinutes, ExpectedRevision: config.Revision}
	if _, e := s.SavePaymentSettings(in, "admin"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SavePaymentSettings(in, "admin"); !errors.Is(e, ErrConflict) {
		t.Fatal("stale write accepted", e)
	}
	in.ExpectedRevision++
	in.MerchantNum = "other"
	if _, e := s.SavePaymentSettings(in, "admin"); e == nil {
		t.Fatal("reused secret for another merchant")
	}
	blank := ""
	in.MerchantNum = config.MerchantNum
	in.Enabled = false
	in.Secret = &blank
	cleared, e := s.SavePaymentSettings(in, "admin")
	if e != nil || cleared.SecretConfigured || cleared.Enabled {
		t.Fatal(cleared, e)
	}
	var history int
	s.DB.QueryRow("SELECT COUNT(*) FROM payment_config_versions WHERE secret!=''").Scan(&history)
	if history != 2 {
		t.Fatal("deleted historical verification keys", history)
	}
}

func TestPaymentDoubleClickCallsProviderOnceAndSnapshotsPrice(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	var calls atomic.Int32
	client := paymentTestClient()
	inner := client.Transport
	client.Transport = paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return inner.RoundTrip(r)
	})
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: "double-click", TemplateID: p.ID, TemplateRevision: p.Revision}, client)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	set := map[string]bool{}
	for id := range ids {
		set[id] = true
	}
	if calls.Load() != 1 || len(set) != 1 {
		t.Fatal("dispatch duplicated", calls.Load(), set)
	}
	o := makePaymentOrder(t, s, u, p, "independent-copy")
	if set[o.ID] || o.AmountCents != 1200 || o.TemplateSnapshot.PriceCents == nil || *o.TemplateSnapshot.PriceCents != 1200 {
		t.Fatal("missing independent purchase snapshot")
	}
	if _, e := s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: "double-click", TemplateID: p.ID, TemplateRevision: p.Revision + 1}, client); !errors.Is(e, ErrConflict) {
		t.Fatal("idempotency key changed purchase", e)
	}
}

func TestPaymentLateClosedDuplicateNotificationsGrantExactlyOneWithOriginalDuration(t *testing.T) {
	s, now, u, p, _ := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "late-notice-order")
	if _, e := s.ClosePaymentOrder(u.ID, o.ID, o.Revision); e != nil {
		t.Fatal(e)
	}
	p.ValidityDays = 99
	rate := float64(20)
	p.BandwidthMbps = &rate
	p, e := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	*now = now.Add(48 * time.Hour)
	v := paymentNotice(o, "test-payment-secret-v1")
	v.Set("attch", "different-order")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.ProcessPaymentNotify(v); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.FulfillPaymentOrders(20); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	result, e := s.PaymentOrder(u.ID, o.ID)
	if e != nil || result.Status != "fulfilled" {
		t.Fatal(result, e)
	}
	var count int
	s.DB.QueryRow("SELECT COUNT(*) FROM portal_subscriptions WHERE grant_key=?", "payment:"+o.ID).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate grant", count)
	}
	sub, e := s.UserSubscription(u.ID, result.SubscriptionID)
	if e != nil || !sub.CreatedAt.Equal(*now) || !sub.ExpiresAt.Equal(now.Add(24*time.Hour)) || *sub.BandwidthMbps != 20 {
		t.Fatal("wrong snapshot duration or live limit", sub, e)
	}
}

func TestPaymentAmountOrderBindingOwnershipAndPlatformMismatch(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "bound-order-key")
	if _, e := s.PaymentOrder("other-user", o.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-user read", e)
	}
	v := paymentNotice(o, "test-payment-secret-v1")
	v.Set("platformOrderNo", "other-platform")
	if e := s.ProcessPaymentNotify(v); !errors.Is(e, ErrPaymentNotify) {
		t.Fatal("known platform ID mismatch accepted", e)
	}
	v = paymentNotice(o, "test-payment-secret-v1")
	v.Set("amount", "1.00")
	h := md5.Sum([]byte("1merchant1" + o.OrderNo + "1.00test-payment-secret-v1"))
	v.Set("sign", hex.EncodeToString(h[:]))
	if e := s.ProcessPaymentNotify(v); !errors.Is(e, ErrPaymentNotify) {
		t.Fatal("validly signed wrong amount accepted", e)
	}
	v = paymentNotice(o, "test-payment-secret-v1")
	v.Add("merchantNum", "other")
	if e := s.ProcessPaymentNotify(v); !errors.Is(e, ErrPaymentNotify) {
		t.Fatal("parameter pollution accepted", e)
	}
	v = paymentNotice(o, "test-payment-secret-v1")
	v.Set("orderNo", "HQOLD123")
	if e := s.ProcessPaymentNotify(v); !errors.Is(e, ErrPaymentNotify) {
		t.Fatal("legacy order accepted", e)
	}
}

func TestPaymentTimeoutNeverRedispatchesAndEarlyNotifySurvivesResponse(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	var calls int
	c := &payment.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("timeout including secret")
	})}
	in := model.PaymentOrderInput{RequestID: "unknown-creation", TemplateID: p.ID, TemplateRevision: p.Revision}
	o, e := s.CreatePaymentOrder(context.Background(), u.ID, in, c)
	if e != nil || o.Status != "unknown" || strings.Contains(o.LastError, "secret") {
		t.Fatal(o, e)
	}
	_, e = s.CreatePaymentOrder(context.Background(), u.ID, in, c)
	if e != nil || calls != 1 {
		t.Fatal("unknown creation redispatched", calls, e)
	}
	c = &payment.Client{Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		v, _ := url.ParseQuery(string(body))
		var id string
		if e := s.DB.QueryRow("SELECT id FROM payment_orders WHERE order_no=?", v.Get("orderNo")).Scan(&id); e != nil {
			t.Fatal(e)
		}
		early, e := s.PaymentOrder(u.ID, id)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.ProcessPaymentNotify(paymentNotice(early, "test-payment-secret-v1")); e != nil {
			t.Fatal(e)
		}
		if _, e = s.FulfillPaymentOrders(10); e != nil {
			t.Fatal(e)
		}
		return paymentTestResponse("p-" + early.OrderNo), nil
	})}
	in.RequestID = "early-callback-order"
	o, e = s.CreatePaymentOrder(context.Background(), u.ID, in, c)
	if e != nil || o.Status != "fulfilled" || o.SubscriptionID == "" || o.PayURL != "" {
		t.Fatal("response overwrote settlement", o, e)
	}
}

func TestPaymentDisabledTemplateBlocksUntilExplicitRetryAndDisabledCheckoutStillSettles(t *testing.T) {
	s, _, u, p, settings := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "blocked-template-order")
	p.Enabled = false
	p, e := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: false, APIBaseURL: settings.APIBaseURL, MerchantNum: settings.MerchantNum, PayType: settings.PayType, PayDurationMinutes: 10, ExpectedRevision: settings.Revision}, "admin"); e != nil {
		t.Fatal(e)
	}
	if e = s.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	o, e = s.PaymentOrder(u.ID, o.ID)
	if e != nil || o.PaymentState != "paid" || o.FulfillmentState != "blocked" {
		t.Fatal(o, e)
	}
	p.Enabled = true
	if _, e = s.SaveSubscriptionTemplate(p.ID, p, p.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	still, _ := s.PaymentOrder(u.ID, o.ID)
	if still.FulfillmentState != "blocked" {
		t.Fatal("silently opened blocked order")
	}
	done, e := s.RetryPaymentFulfillment(o.ID, o.Revision, "admin")
	if e != nil || done.Status != "fulfilled" {
		t.Fatal(done, e)
	}
}

func TestPaymentHistoricalKeysAreScopedToProviderIdentity(t *testing.T) {
	s, _, u, p, settings := paymentFixture(t)
	a := makePaymentOrder(t, s, u, p, "old-key-payment")
	b := makePaymentOrder(t, s, u, p, "new-key-payment")
	newSecret := "test-payment-secret-v2"
	settings, e := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: settings.APIBaseURL, MerchantNum: settings.MerchantNum, PayType: settings.PayType, PayDurationMinutes: 10, Secret: &newSecret, ExpectedRevision: settings.Revision}, "admin")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ProcessPaymentNotify(paymentNotice(a, "test-payment-secret-v1")); e != nil {
		t.Fatal("old signature lost", e)
	}
	if e = s.ProcessPaymentNotify(paymentNotice(b, newSecret)); e != nil {
		t.Fatal("current merchant signature rejected", e)
	}
	c := makePaymentOrder(t, s, u, p, "foreign-key-payment")
	foreign := "different-provider-secret"
	if _, e = s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: true, APIBaseURL: "https://other.example.com", MerchantNum: settings.MerchantNum, PayType: settings.PayType, PayDurationMinutes: 10, Secret: &foreign, ExpectedRevision: settings.Revision}, "admin"); e != nil {
		t.Fatal(e)
	}
	if e = s.ProcessPaymentNotify(paymentNotice(c, foreign)); !errors.Is(e, ErrPaymentNotify) {
		t.Fatal("foreign provider secret authorized old order", e)
	}
}

func TestPaymentUnpricedHiddenAndQueryDisabledDoNotContactProvider(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "manual-query-gate")
	client := &payment.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected provider call")
		return nil, errors.New("unexpected")
	})}
	if _, e := s.ReconcilePaymentOrder(context.Background(), o.ID, o.Revision, "admin", client); !errors.Is(e, ErrPaymentQueryDisabled) {
		t.Fatal(e)
	}
	p.PriceCents = nil
	p, e := s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: "unpriced-template", TemplateID: p.ID, TemplateRevision: p.Revision}, client); e == nil {
		t.Fatal("unpriced template sold")
	}
	price := int64(100)
	p.PriceCents = &price
	p.Listed = false
	p, e = s.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: "hidden-template", TemplateID: p.ID, TemplateRevision: p.Revision}, client); e == nil {
		t.Fatal("hidden template sold")
	}
}

func TestPaymentUnsignedChannelDoesNotRejectAuthenticatedReceipt(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "pool-channel-order")
	notice := paymentNotice(o, "test-payment-secret-v1")
	notice.Set("type", "alipay-actual-pool-channel")
	if e := s.ProcessPaymentNotify(notice); e != nil {
		t.Fatal("unsigned pool metadata prevented settlement", e)
	}
	if _, e := s.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	result, _ := s.PaymentOrder(u.ID, o.ID)
	if result.Status != "fulfilled" {
		t.Fatal(result)
	}
}

func TestPaymentOneFulfillmentErrorDoesNotStarveLaterOrders(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	a := makePaymentOrder(t, s, u, p, "bad-historical-order")
	b := makePaymentOrder(t, s, u, p, "healthy-later-order")
	for _, o := range []model.PaymentOrder{a, b} {
		if e := s.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); e != nil {
			t.Fatal(e)
		}
	}
	// Simulate one malformed legacy duration while leaving the order record
	// readable, so the worker must record its failure and continue the batch.
	if _, e := s.DB.Exec("UPDATE payment_orders SET validity_days=40000 WHERE id=?", a.ID); e != nil {
		t.Fatal(e)
	}
	done, e := s.FulfillPaymentOrders(10)
	if e == nil || done != 1 {
		t.Fatal("failed order starved batch", done, e)
	}
	bad, _ := s.PaymentOrder(u.ID, a.ID)
	good, _ := s.PaymentOrder(u.ID, b.ID)
	if bad.FulfillmentState != "blocked" || bad.LastError == "" || good.Status != "fulfilled" {
		t.Fatal(bad, good)
	}
}

func enablePaymentQuery(t *testing.T, s *Store, settings model.PaymentSettings) {
	t.Helper()
	_, e := s.SavePaymentSettings(model.PaymentSettingsInput{Enabled: settings.Enabled, APIBaseURL: settings.APIBaseURL, MerchantNum: settings.MerchantNum, PayType: settings.PayType, PayDurationMinutes: settings.PayDurationMinutes, QueryEnabled: true, ExpectedRevision: settings.Revision}, "admin")
	if e != nil {
		t.Fatal(e)
	}
}

func TestPaymentQueryCloseAndCallbackRaceNeverRegressesPaid(t *testing.T) {
	s, now, u, p, settings := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "provider-close-order")
	enablePaymentQuery(t, s, settings)
	queryClient := func(order model.PaymentOrder, notify bool) *payment.Client {
		return &payment.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) {
			if notify {
				if e := s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); e != nil {
					t.Fatal(e)
				}
			}
			body, _ := json.Marshal(map[string]any{"success": true, "code": 200, "data": map[string]string{"orderId": order.PlatformOrderNo, "orderNo": order.OrderNo, "merchantNum": "merchant1", "amount": payment.FormatAmount(order.AmountCents), "orderState": "7", "payType": "actual-pool-channel"}})
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})}
	}
	closed, e := s.ReconcilePaymentOrder(context.Background(), o.ID, o.Revision, "admin", queryClient(o, false))
	if e != nil || closed.Status != "closed" || closed.PayURL != "" {
		t.Fatal("provider close not reflected", closed, e)
	}
	*now = now.Add(time.Minute)
	other := makePaymentOrder(t, s, u, p, "callback-racing-query")
	paid, e := s.ReconcilePaymentOrder(context.Background(), other.ID, other.Revision, "admin", queryClient(other, true))
	if e != nil || paid.PaymentState != "paid" || paid.Status != "paid_pending" {
		t.Fatal("stale query regressed callback", paid, e)
	}
}

func TestPaymentMigrationCancelsOnlyExistingRequestsOnce(t *testing.T) {
	s, _, u, p, _, _ := portalFixture(t, 1, nil)
	r, e := s.CreateSubscriptionRequest(u.ID, p.ID, "old-pending-request", p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	// Recreate the pre-payment marker condition without touching identities or
	// accounting, as a production upgrade encounters this exact schema state.
	if _, e = s.DB.Exec("DELETE FROM metadata WHERE key='payments_schema_version'"); e != nil {
		t.Fatal(e)
	}
	if e = EnsurePaymentSchema(s.DB); e != nil {
		t.Fatal(e)
	}
	var status, note string
	if e = s.DB.QueryRow("SELECT status,note FROM portal_requests WHERE id=?", r.ID).Scan(&status, &note); e != nil || status != "cancelled" || note == "" {
		t.Fatal(status, note, e)
	}
	newer, e := s.CreateSubscriptionRequest(u.ID, p.ID, "later-request-row", p.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if e = EnsurePaymentSchema(s.DB); e != nil {
		t.Fatal(e)
	}
	if e = s.DB.QueryRow("SELECT status FROM portal_requests WHERE id=?", newer.ID).Scan(&status); e != nil || status != "pending" {
		t.Fatal("repeated initialization rewrote history", status, e)
	}
}

func TestPaymentTemplateWithoutNodesCannotFulfillEmptySubscription(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "no-nodes-after-payment")
	p.NodeIDs = nil
	p.DefaultNodeID = ""
	if _, e := s.SaveSubscriptionTemplate(p.ID, p, p.Revision); e != nil {
		t.Fatal(e)
	}
	if e := s.ProcessPaymentNotify(paymentNotice(o, "test-payment-secret-v1")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.FulfillPaymentOrders(10); e != nil {
		t.Fatal(e)
	}
	result, e := s.PaymentOrder(u.ID, o.ID)
	if e != nil || result.FulfillmentState != "blocked" || result.SubscriptionID != "" {
		t.Fatal("empty subscription granted", result, e)
	}
}

func TestPaymentLateCreateResponseCannotOverwriteReconciledPlatformIdentity(t *testing.T) {
	for _, failure := range []bool{false, true} {
		label := "different-id"
		if failure {
			label = "timeout"
		}
		t.Run(label, func(t *testing.T) {
			s, _, u, p, settings := paymentFixture(t)
			enablePaymentQuery(t, s, settings)
			client := &payment.Client{Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				v, _ := url.ParseQuery(string(body))
				var id string
				if e := s.DB.QueryRow("SELECT id FROM payment_orders WHERE order_no=?", v.Get("orderNo")).Scan(&id); e != nil {
					t.Fatal(e)
				}
				pending, e := s.PaymentOrder(u.ID, id)
				if e != nil {
					t.Fatal(e)
				}
				query := &payment.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) {
					payload, _ := json.Marshal(map[string]any{"success": true, "code": 200, "data": map[string]string{"orderId": "trusted-query-id", "orderNo": pending.OrderNo, "merchantNum": "merchant1", "amount": payment.FormatAmount(pending.AmountCents), "tradeMoney": "11.98", "orderState": "4", "payType": "pool-actual-type"}})
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
				})}
				if _, e = s.ReconcilePaymentOrder(context.Background(), pending.ID, pending.Revision, "admin", query); e != nil {
					t.Fatal(e)
				}
				if failure {
					return nil, errors.New("provider timeout")
				}
				return paymentTestResponse("different-late-id"), nil
			})}
			out, e := s.CreatePaymentOrder(context.Background(), u.ID, model.PaymentOrderInput{RequestID: "reconciled-while-creating", TemplateID: p.ID, TemplateRevision: p.Revision}, client)
			if e != nil || out.PlatformOrderNo != "trusted-query-id" || out.PaymentState != "paid" || out.CreationState != "unknown" || out.PayURL != "" {
				t.Fatal("late creation replaced trusted reconciliation", out, e)
			}
			found := false
			for _, a := range out.Activities {
				if a.Kind == "paid.query" {
					found = true
					if a.ActualAmountCents == nil || *a.ActualAmountCents != 1198 {
						t.Fatal("query reported amount omitted", a)
					}
				}
			}
			if !found {
				t.Fatal("missing query payment activity")
			}
		})
	}
}

func TestPaymentActivityExposesReportedAmountWithoutChangingPrice(t *testing.T) {
	s, _, u, p, _ := paymentFixture(t)
	o := makePaymentOrder(t, s, u, p, "reported-actual-amount")
	v := paymentNotice(o, "test-payment-secret-v1")
	v.Set("actualPayAmount", "11.98")
	if e := s.ProcessPaymentNotify(v); e != nil {
		t.Fatal(e)
	}
	result, e := s.PaymentOrder(u.ID, o.ID)
	if e != nil || result.AmountCents != 1200 {
		t.Fatal("reported floating amount changed price", result, e)
	}
	count := 0
	for _, activity := range result.Activities {
		if activity.Kind == "paid.notify" {
			count++
			if activity.ActualAmountCents == nil || *activity.ActualAmountCents != 1198 {
				t.Fatal("missing reported actual amount", activity)
			}
		} else if activity.ActualAmountCents != nil {
			t.Fatal("non-payment activity has a reported amount", activity)
		}
	}
	if count != 1 {
		t.Fatal("missing receipt activity", count)
	}
}

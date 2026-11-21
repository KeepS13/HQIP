package control

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
)

func TestRenewalRoutesRequireOwnerCSRFAndReturnOriginalSubscription(t *testing.T) {
	s, admin := newServer(t)
	ac, csrf := loginAdmin(t, s, admin)
	u, cookie, userCSRF := portalLoginFixture(t, s, "renewal-owner@example.com")
	_, foreignCookie, foreignCSRF := portalLoginFixture(t, s, "renewal-other@example.com")
	sub := subscriptionFixture(t, s, u, nil, "", 2)
	// Model an existing purchased subscription, with its original allocation.
	if _, err := s.Store.DB.Exec("UPDATE portal_subscriptions SET grant_key='payment:fixture' WHERE id=?", sub.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.Store.SubscriptionTemplate(sub.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	price := int64(100)
	p.PriceCents = &price
	p, err = s.Store.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Store.UserSubscription(u.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"enabled": true, "apiBaseUrl": "https://pay.example.com", "merchantNum": "testmerchant", "payType": "aloop", "payDurationMinutes": 10, "secret": "test-payment-secret-long-enough", "expectedRevision": 0}
	if w := request(admin, "PUT", "/api/v1/settings/payment", settings, ac, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	calls := 0
	s.opts.PaymentClient = &payment.Client{Transport: portalTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"success":true,"code":200,"data":{"id":"renewal-platform","payUrl":"https://pay.example.com/renewal"}}`))}, nil
	})}
	pub := s.PublicHandler()
	body := model.PaymentOrderInput{RequestID: "renewal-route-request-0001", TemplateID: p.ID, TemplateRevision: p.Revision, RenewalSubscriptionID: sub.ID, ExpectedSubscriptionRevision: current.Revision}
	if w := portalRequest(pub, "POST", "/api/v1/user/orders", body, cookie, "", s.opts.UserOrigin); w.Code != 403 {
		t.Fatal("renewal skipped CSRF", w.Code)
	}
	if w := portalRequest(pub, "POST", "/api/v1/user/orders", body, foreignCookie, foreignCSRF, s.opts.UserOrigin); w.Code != 404 {
		t.Fatal("cross-user renewal", w.Code, w.Body.String())
	}
	var order model.PaymentOrder
	for i := 0; i < 2; i++ {
		w := portalRequest(pub, "POST", "/api/v1/user/orders", body, cookie, userCSRF, s.opts.UserOrigin)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got model.PaymentOrder
		if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.OrderType != "renewal" || got.RenewalSubscriptionID != sub.ID || i > 0 && got.ID != order.ID {
			t.Fatal(got)
		}
		order = got
	}
	if calls != 1 {
		t.Fatal("duplicate dispatch", calls)
	}
	values := url.Values{"merchantNum": {"testmerchant"}, "orderNo": {order.OrderNo}, "amount": {"1.00"}, "state": {"1"}, "platformOrderNo": {"renewal-platform"}, "actualPayAmount": {"1.00"}, "payTime": {"2020-01-01 00:00:00"}, "type": {"alipay"}}
	sign := md5.Sum([]byte("1testmerchant" + order.OrderNo + "1.00test-payment-secret-long-enough"))
	values.Set("sign", hex.EncodeToString(sign[:]))
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/api/v1/payments/fm/notify", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		pub.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != "success" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, w := range []*httptest.ResponseRecorder{
		portalRequest(pub, "GET", "/api/v1/user/orders/"+order.ID, nil, cookie, userCSRF, s.opts.UserOrigin),
		request(admin, "GET", "/api/v1/orders/"+order.ID, nil, ac, csrf),
	} {
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got model.PaymentOrder
		if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != "fulfilled" || got.OrderType != "renewal" || got.SubscriptionID != sub.ID || got.PeriodStartsAt == nil || !got.PeriodStartsAt.Equal(current.ExpiresAt) || got.PeriodEndsAt == nil {
			t.Fatal(got)
		}
	}
	subs, err := s.Store.UserSubscriptions(u.ID)
	if err != nil || len(subs) != 1 || len(subs[0].Periods) != 2 {
		t.Fatal(subs, err)
	}
}

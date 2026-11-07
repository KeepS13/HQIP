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

func TestPaymentRoutesAndAutomaticIndependentGrant(t *testing.T) {
	s, admin := newServer(t)
	cookie, csrf := loginAdmin(t, s, admin)
	u, uc, ucsrf := portalLoginFixture(t, s, "payment-buyer@example.com")
	_, otherCookie, otherCSRF := portalLoginFixture(t, s, "payment-other@example.com")
	initial := subscriptionFixture(t, s, u, nil, "", 2)
	p, err := s.Store.SubscriptionTemplate(initial.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	price := int64(100)
	p.PriceCents = &price
	p, err = s.Store.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	pub := s.PublicHandler()
	settings := map[string]any{"enabled": true, "apiBaseUrl": "https://pay.example.com", "merchantNum": "testmerchant", "payType": "aloop", "payDurationMinutes": 10, "queryEnabled": false, "secret": "test-payment-secret-long-enough", "expectedRevision": 0}
	if w := request(admin, "PUT", "/api/v1/settings/payment", settings, cookie, ""); w.Code != 403 {
		t.Fatal("settings skipped csrf", w.Code)
	}
	w := request(admin, "PUT", "/api/v1/settings/payment", settings, cookie, csrf)
	if w.Code != 200 || strings.Contains(w.Body.String(), "test-payment-secret-long-enough") {
		t.Fatal("settings failed or leaked secret", w.Code)
	}
	if w = request(admin, "PUT", "/api/v1/settings/payment", settings, cookie, csrf); w.Code != 409 {
		t.Fatal("stale settings accepted", w.Code)
	}
	if w = request(pub, "GET", "/api/v1/settings/payment", nil, uc, ""); w.Code != 404 {
		t.Fatal("public settings exposed", w.Code)
	}
	if w = request(admin, "GET", "/api/v1/orders", nil, uc, ""); w.Code != 401 {
		t.Fatal("user cookie accepted as admin", w.Code)
	}
	calls := 0
	s.opts.PaymentClient = &payment.Client{Transport: portalTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/startOrder" {
			t.Fatal("unexpected provider request")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(r.Form.Get("returnUrl"), s.opts.UserOrigin+"/orders/") || r.Form.Get("apiMode") != "post_form" {
			t.Fatal("wrong callback/return contract")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"success":true,"code":200,"data":{"id":"platform1","payUrl":"https://pay.example.com/checkout/1"}}`))}, nil
	})}
	body := map[string]any{"templateId": p.ID, "templateRevision": p.Revision, "requestId": "browser-payment-request-0001"}
	w = portalRequest(pub, "POST", "/api/v1/user/orders", body, uc, ucsrf, s.opts.UserOrigin)
	if w.Code != 200 {
		t.Fatal("create", w.Code, w.Body.String())
	}
	var order model.PaymentOrder
	if err = json.Unmarshal(w.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	w = portalRequest(pub, "POST", "/api/v1/user/orders", body, uc, ucsrf, s.opts.UserOrigin)
	if w.Code != 200 || calls != 1 {
		t.Fatal("duplicate provider dispatch", w.Code, calls)
	}
	for _, path := range []string{"/api/v1/user/orders/" + order.ID, "/api/v1/user/orders/" + order.ID + "/close"} {
		method := "GET"
		if strings.HasSuffix(path, "/close") {
			method = "POST"
		}
		if w = portalRequest(pub, method, path, map[string]any{"expectedRevision": order.Revision}, otherCookie, otherCSRF, s.opts.UserOrigin); w.Code != 404 {
			t.Fatal("cross-user order access", w.Code)
		}
	}
	values := url.Values{"merchantNum": {"testmerchant"}, "orderNo": {order.OrderNo}, "amount": {"1.00"}, "state": {"1"}, "platformOrderNo": {"platform1"}, "actualPayAmount": {"0.99"}, "payTime": {"2020-01-01 00:00:00"}, "type": {"alipay"}, "attch": {"some-other-order"}}
	sign := md5.Sum([]byte("1testmerchant" + order.OrderNo + "1.00test-payment-secret-long-enough"))
	values.Set("sign", hex.EncodeToString(sign[:]))
	notify := func(v url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/payments/fm/notify", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		pub.ServeHTTP(w, r)
		return w
	}
	values.Set("amount", "2.00")
	if w = notify(values); w.Code == 200 {
		t.Fatal("tampered amount accepted")
	}
	values.Set("amount", "1.00")
	for i := 0; i < 2; i++ {
		w = notify(values)
		if w.Code != 200 || w.Body.String() != "success" {
			t.Fatal("callback", w.Code, w.Body.String())
		}
	}
	values.Add("amount", "1.00")
	if w = notify(values); w.Code == 200 {
		t.Fatal("ambiguous signed field accepted")
	}
	values.Set("amount", "1.00")
	if _, err = s.Store.FulfillPaymentOrders(20); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.FulfillPaymentOrders(20); err != nil {
		t.Fatal(err)
	}
	subs, err := s.Store.UserSubscriptions(u.ID)
	if err != nil || len(subs) != 2 {
		t.Fatal("payment did not grant exactly one independent subscription", len(subs), err)
	}
	got, err := s.Store.PaymentOrder(u.ID, order.ID)
	if err != nil || got.Status != "fulfilled" || got.SubscriptionID == "" {
		t.Fatal("order not fulfilled", got.Status, err)
	}
	if got.FulfilledAt == nil || got.FulfilledAt.Year() < 2025 {
		t.Fatal("untrusted payTime set activation time")
	}
	w = portalRequest(pub, "GET", "/api/v1/user/payment-info", nil, uc, ucsrf, s.opts.UserOrigin)
	if w.Code != 200 || strings.Contains(w.Body.String(), "merchant") || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("public payment info leaked configuration")
	}
}

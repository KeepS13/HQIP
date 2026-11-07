package payment

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func configFixture() Config {
	return Config{APIBaseURL: "https://pay.example.com", MerchantNum: "merchant1", Secret: "secret-value-123", PayType: "aloop", PayDurationMinutes: 10}
}
func requestFixture() CreateRequest {
	return CreateRequest{Config: configFixture(), OrderID: "local-id", OrderNo: "HQN0123", AmountCents: 1000, Subject: "订阅", NotifyURL: "https://api.example.com/api/v1/payments/fm/notify"}
}

func TestCreateSignsExactAmountAndKeepsReturnOnUserOrigin(t *testing.T) {
	called := 0
	c := &Client{ReturnOrigin: "https://user.example.com", Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		called++
		if r.Method != "POST" || r.URL.String() != "https://pay.example.com/startOrder" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatal("wrong provider request")
		}
		body, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("sign") != "564f8a2a8d0ccb53fedf3f81d7fb3cba" || form.Get("amount") != "10.00" || form.Get("apiMode") != "post_form" || form.Get("returnUrl") != "https://user.example.com/orders/local-id" || form.Get("payDuration") != "10" || strings.Contains(string(body), configFixture().Secret) {
			t.Fatal("wrong payment encoding/signing")
		}
		return jsonResponse(`{"success":true,"code":200,"data":{"id":"provider-1","payUrl":"https://pay.example.com/pay/1"}}`), nil
	})}
	out, err := c.Create(context.Background(), requestFixture())
	if err != nil || out.PlatformOrderNo != "provider-1" || called != 1 {
		t.Fatal(out, err, called)
	}
}

func TestNotifySignatureDoesNotTrustMetadataAndRejectsPollution(t *testing.T) {
	v := url.Values{"merchantNum": {"merchant1"}, "orderNo": {"HQN0123"}, "amount": {"10.00"}, "state": {"1"}, "type": {"alipay"}, "platformOrderNo": {"provider-1"}, "actualPayAmount": {"9.99"}, "payTime": {"2026-11-07 12:00:00"}, "sign": {"88ef73a7a1905545553e484e079947b8"}, "attch": {"another-order"}}
	p, err := VerifyNotify(v, configFixture().Secret)
	if err != nil || p.OrderNo != "HQN0123" || p.AmountCents != 1000 || p.ActualAmountCents != 999 {
		t.Fatal(p, err)
	}
	v.Set("amount", "1.00")
	if _, err = VerifyNotify(v, configFixture().Secret); err == nil {
		t.Fatal("tampered signed amount accepted")
	}
	v.Set("amount", "10.00")
	v.Add("orderNo", "other")
	if _, err = VerifyNotify(v, configFixture().Secret); err == nil {
		t.Fatal("duplicate parameters accepted")
	}
}

func TestPaymentURLsBlockLocalMetadataAndUntrustedSchemes(t *testing.T) {
	for _, raw := range []string{"http://pay.example.com", "https://localhost", "https://localhost.example.local", "https://127.0.0.1", "https://[::ffff:127.0.0.1]", "https://169.254.169.254", "https://100.100.100.200", "https://user:pass@pay.example.com", "https://pay.example.com:8443", "javascript:alert(1)", "https://pay.example.com/#fragment"} {
		if ValidateURL(raw) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://pay.example.com/", "https://pay.example.com/api", "https://1.1.1.1/pay"} {
		if err := ValidateURL(raw); err != nil {
			t.Error(raw, err)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.2", "192.168.1.1", "::1", "fc00::1", "64:ff9b::7f00:1", "2002:7f00:1::", "169.254.169.254"} {
		if publicAddress(netip.MustParseAddr(ip)) {
			t.Error("private DNS address accepted", ip)
		}
	}
	if !publicAddress(netip.MustParseAddr("1.1.1.1")) || !publicAddress(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("public address rejected")
	}
}

func TestProviderErrorsAreSanitizedAndNeverFollowRedirects(t *testing.T) {
	cases := []struct {
		name, body     string
		status         int
		transportError bool
		uncertain      bool
	}{
		{"business-failure", `{"success":false,"code":500,"msg":"SECRET in upstream error"}`, 200, false, false},
		{"malformed", "SECRET broken body", 200, false, true},
		{"missing-result", `{}`, 200, false, true},
		{"HTTP-failure", "SECRET upstream failure", 500, false, true},
		{"unsafe-url", `{"success":true,"code":200,"data":{"id":"provider-1","payUrl":"javascript:alert('SECRET')"}}`, 200, false, true},
		{"redirect", "SECRET", 302, false, true},
		{"transport", "", 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := &Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if tc.transportError {
					return nil, errors.New("SECRET transport error")
				}
				r := jsonResponse(tc.body)
				r.StatusCode = tc.status
				r.Header.Set("Location", "https://elsewhere.example.com/")
				return r, nil
			})}
			_, err := c.Create(context.Background(), requestFixture())
			var safe *ProviderError
			if !errors.As(err, &safe) || safe.Uncertain != tc.uncertain || strings.Contains(err.Error(), "SECRET") || calls != 1 {
				t.Fatal("unsafe provider failure", err, calls)
			}
		})
	}
}

func TestMoneyParsingIsIntegerOnly(t *testing.T) {
	for text, want := range map[string]int64{"1": 100, "1.1": 110, "1.01": 101, "1000000.00": 100000000} {
		got, e := ParseAmount(text)
		if e != nil || got != want {
			t.Fatal(text, got, e)
		}
	}
	for _, s := range []string{"-1", "NaN", "1e2", "1.001", " 1", "99999999999999", "+1", "1."} {
		if _, e := ParseAmount(s); e == nil {
			t.Error("invalid decimal accepted", s)
		}
	}
}

func TestQueryChecksProtocolAndUsesNoPlaintextSecret(t *testing.T) {
	c := &Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		f, _ := url.ParseQuery(string(body))
		if r.URL.Path != "/queryOutOrder" || f.Get("orderNo") != "HQN0123" || strings.Contains(string(body), configFixture().Secret) {
			t.Fatal("bad query request")
		}
		return jsonResponse(`{"success":true,"code":200,"data":{"orderId":"p1","orderNo":"HQN0123","merchantNum":"merchant1","amount":"10.00","tradeMoney":"9.99","orderState":"4","payType":"alipay","payTime":"2026-11-07 12:00:00"}}`), nil
	})}
	r, e := c.Query(context.Background(), configFixture(), "HQN0123")
	if e != nil || r.State != "4" || r.AmountCents != 1000 || r.ActualAmountCents != 999 {
		t.Fatal(r, e)
	}
}

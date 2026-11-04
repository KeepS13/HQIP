package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const testWebhook = "https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef"

func TestSignatureAndStrictDestination(t *testing.T) {
	if got := Sign("1600000000", "test-secret"); got != "BwqgZGo8lGLJAUAc/kiJR0TDtnLVnsITtszoHeRuXV0=" {
		t.Fatal(got)
	}
	if Sign("1600000001", "test-secret") == Sign("1600000000", "test-secret") || Sign("1600000000", "wrong-secret") == Sign("1600000000", "test-secret") {
		t.Fatal("signature ignored time or secret")
	}
	for _, raw := range []string{testWebhook, strings.Replace(testWebhook, ".cn/", ".cn:443/", 1)} {
		if err := ValidateWebhook(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		strings.Replace(testWebhook, "https:", "http:", 1), strings.Replace(testWebhook, "open.feishu.cn", "127.0.0.1", 1), strings.Replace(testWebhook, "open.feishu.cn", "open.feishu.cn.evil.test", 1), strings.Replace(testWebhook, "open.feishu.cn", "user:pass@open.feishu.cn", 1), testWebhook + "?", testWebhook + "?token=x", testWebhook + "#x", testWebhook + "/x", strings.Replace(testWebhook, ".cn/", ".cn:8443/", 1), strings.Replace(testWebhook, "hook/", "hook/%2f", 1), testWebhook + "\n",
	} {
		if ValidateWebhook(raw) == nil {
			t.Fatalf("accepted unsafe target: %q", raw)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "192.0.2.1", "198.18.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "2002:7f00:1::"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Fatalf("unsafe IP %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700::1111"} {
		if !publicIP(netip.MustParseAddr(ip)) {
			t.Fatalf("public IP rejected %s", ip)
		}
	}
	if conn, err := publicDial(context.Background(), "tcp", "127.0.0.1:443"); err == nil || conn != nil {
		t.Fatal("dial accepted arbitrary host")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSignedPayloadAndSafeBusinessResults(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		body           string
		success, retry bool
		code           int
	}{
		{"accepted", 200, `{"code":0}`, true, false, 0},
		{"signature", 200, `{"code":19021,"msg":"secret-do-not-echo"}`, false, false, 19021},
		{"ip", 200, `{"code":19022}`, false, false, 19022},
		{"keyword", 200, `{"code":19024}`, false, false, 19024},
		{"format", 200, `{"code":9499}`, false, false, 9499},
		{"limit", 200, `{"code":11232}`, false, true, 11232},
		{"http-limit", 429, `{}`, false, true, 0},
		{"server-error", 502, `secret-do-not-echo`, false, true, 0},
		{"unauthorized", 403, `secret-do-not-echo`, false, false, 0},
		{"missing-code", 200, `{"StatusCode":0}`, false, false, 0},
		{"null-code", 200, `{"code":null}`, false, false, 0},
		{"bad-json", 200, `not-json`, false, false, 0},
		{"too-large", 200, strings.Repeat("x", 33*1024), false, false, 0},
		{"redirect", 302, ``, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Unix(1600000000, 0)
			s := New()
			s.now = func() time.Time { return at }
			s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != testWebhook || r.Method != "POST" {
					t.Fatal("wrong request")
				}
				data, _ := io.ReadAll(r.Body)
				var body map[string]any
				if json.Unmarshal(data, &body) != nil || body["timestamp"] != "1600000000" || body["sign"] != Sign("1600000000", "test-secret") {
					t.Fatal("wrong signature")
				}
				if strings.Contains(string(data), "test-secret") || strings.Contains(string(data), "lark_md") || strings.Contains(string(data), "evil.test") {
					t.Fatal("unsafe card")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})
			got := s.Send(context.Background(), testWebhook, "test-secret", Message{BaseURL: "https://hqip.5202345.xyz", Events: []Event{{ID: "event-1", Title: "<at id=all>管理员</at>", Message: "测试消息", DetailPath: "//evil.test", CreatedAt: at}}})
			if got.Success != tc.success || got.Retryable != tc.retry || got.Code != tc.code || strings.Contains(got.Error, "secret-do-not-echo") {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestSenderRetriesRefreshSignatureAndNeverEchoesTransportError(t *testing.T) {
	at := time.Unix(1600000000, 0)
	s := New()
	s.now = func() time.Time { return at }
	var signatures []string
	s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		signatures = append(signatures, v["sign"].(string))
		return nil, errors.New(testWebhook + " sensitive-secret")
	})
	m := Message{Events: []Event{{ID: "event-1", CreatedAt: at}}}
	for i := 0; i < 2; i++ {
		got := s.Send(context.Background(), testWebhook, "secret", m)
		if !got.Retryable || strings.Contains(got.Error, "sensitive-secret") || strings.Contains(got.Error, testWebhook) {
			t.Fatalf("%+v", got)
		}
		at = at.Add(time.Minute)
	}
	if signatures[0] == signatures[1] {
		t.Fatal("retry reused expired signature")
	}
	if s.client.Timeout != 10*time.Second {
		t.Fatal("missing deadline")
	}
	if s.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects enabled")
	}
}

func TestCardBoundAndHTTPSOnlyLinks(t *testing.T) {
	at := time.Unix(1600000000, 0)
	m := Message{BaseURL: "https://hqip.5202345.xyz"}
	for i := 0; i < 8; i++ {
		m.Events = append(m.Events, Event{ID: "event", Level: "danger", Title: strings.Repeat("名", 500), Message: strings.Repeat("流", 3000), DetailPath: "/admin/nodes/abc", CreatedAt: at})
	}
	data, err := payload(at, "secret", m)
	if err != nil || len(data) > 20*1024 {
		t.Fatalf("%d %v", len(data), err)
	}
	if !strings.Contains(string(data), "2020-09-13 20:26:40") || !strings.Contains(string(data), "/admin/nodes/abc") {
		t.Fatal("missing event timestamp/link")
	}
	for _, base := range []string{"http://example.com", "https://x:y@example.com", "https://example.com/path", "https://example.com/?secret=x"} {
		if link(base, "/admin/notifications") != "" {
			t.Fatal("unsafe origin")
		}
	}
	for _, path := range []string{"//evil.test", "/admin/../sub/token", "/sub/token", "/admin/users/a?token=b"} {
		if got := link(m.BaseURL, path); got != "https://hqip.5202345.xyz/admin/notifications" {
			t.Fatal(got)
		}
	}
}

func TestHTTPRedirectIsNotFollowed(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.Write([]byte(`{"code":0}`)) }))
	defer target.Close()
	s := New()
	s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{target.URL}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	result := s.Send(context.Background(), testWebhook, "secret", Message{Events: []Event{{ID: "1", CreatedAt: time.Now()}}})
	if result.Success || hits != 0 {
		t.Fatal("followed redirect")
	}
}

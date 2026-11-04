package control

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/feishu"
	"github.com/KeepS13/HQIP/internal/model"
)

const notificationTestURL = "https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef"

type fakeNotificationSender struct {
	response feishu.Result
	calls    int
	messages []feishu.Message
}

func (f *fakeNotificationSender) Send(ctx context.Context, webhook, secret string, message feishu.Message) feishu.Result {
	f.calls++
	f.messages = append(f.messages, message)
	return f.response
}

func TestNotificationAdminIsolationAndSettings(t *testing.T) {
	s, _ := newServer(t)
	h := s.AdminHandler()
	public := s.PublicHandler()
	paths := []struct{ method, path string }{
		{"GET", "/api/v1/settings/feishu"}, {"PUT", "/api/v1/settings/feishu"}, {"POST", "/api/v1/settings/feishu/test"}, {"GET", "/api/v1/notifications"}, {"POST", "/api/v1/notifications/event/retry"},
	}
	for _, p := range paths {
		if w := request(h, p.method, p.path, map[string]any{}, "", ""); w.Code != 401 {
			t.Fatalf("unauth %s: %d", p.path, w.Code)
		}
		if w := request(public, p.method, p.path, map[string]any{}, "", ""); w.Code != 404 {
			t.Fatalf("public %s: %d", p.path, w.Code)
		}
	}
	cookie, csrf := loginAdmin(t, s, h)
	for _, p := range paths {
		if p.method != "GET" {
			if w := request(h, p.method, p.path, map[string]any{}, cookie, ""); w.Code != 403 {
				t.Fatalf("CSRF %s: %d", p.path, w.Code)
			}
		}
		r := httptest.NewRequest(p.method, p.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("Cookie", "hqip_user_session=user-token; hqip_agent=agent-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("other auth %s: %d", p.path, w.Code)
		}
	}
	r := httptest.NewRequest("PUT", "/api/v1/settings/feishu", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cookie", cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
	w = request(h, "GET", "/api/v1/settings/feishu", nil, cookie, "")
	var cfg model.FeishuSettings
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cfg) != nil || cfg.Enabled || cfg.WebhookConfigured || !cfg.Categories.System {
		t.Fatalf("defaults: %s", w.Body.String())
	}
	input := model.FeishuSettingsInput{Webhook: notificationTestURL, Secret: "test-signature-secret-not-echoed", Categories: model.DefaultNotificationCategories(), ExpectedRevision: cfg.Revision}
	w = request(h, "PUT", "/api/v1/settings/feishu", input, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, forbidden := range []string{notificationTestURL, input.Secret, "01234567-89ab"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("credential in response")
		}
	}
	if w = request(h, "PUT", "/api/v1/settings/feishu", input, cookie, csrf); w.Code != 409 {
		t.Fatalf("stale revision %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{"?page=0", "?page=no", "?pageSize=999", "?pageSize=0"} {
		if w = request(h, "GET", "/api/v1/notifications"+query, nil, cookie, ""); w.Code != 400 {
			t.Fatal("invalid pagination accepted")
		}
	}
}

func TestNotificationTestQueueAndBusinessFailure(t *testing.T) {
	s, _ := newServer(t)
	h := s.AdminHandler()
	cookie, csrf := loginAdmin(t, s, h)
	at := time.Now()
	s.Store.Now = func() time.Time { return at }
	input := model.FeishuSettingsInput{Webhook: notificationTestURL, Secret: "test-signature-secret", Categories: model.DefaultNotificationCategories()}
	if w := request(h, "PUT", "/api/v1/settings/feishu", input, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := request(h, "POST", "/api/v1/settings/feishu/test", map[string]any{}, cookie, csrf)
	if w.Code != 202 {
		t.Fatalf("test %d %s", w.Code, w.Body.String())
	}
	var record model.NotificationRecord
	_ = json.Unmarshal(w.Body.Bytes(), &record)
	if w = request(h, "POST", "/api/v1/settings/feishu/test", map[string]any{}, cookie, csrf); w.Code != 429 {
		t.Fatalf("test flood %d %s", w.Code, w.Body.String())
	}
	sender := &fakeNotificationSender{response: feishu.Result{Error: "飞书签名校验失败（19021）"}}
	var scan time.Time
	if err := s.notificationTick(context.Background(), sender, &scan); err != nil {
		t.Fatal(err)
	}
	page, err := s.Store.Notifications(1, 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != "failed" || sender.calls != 1 {
		t.Fatalf("failure %+v %v", page, err)
	}
	if w = request(h, "POST", "/api/v1/notifications/"+record.ID+"/retry", map[string]any{}, cookie, csrf); w.Code != 202 {
		t.Fatalf("retry %d %s", w.Code, w.Body.String())
	}
	at = at.Add(31 * time.Second)
	sender.response = feishu.Result{Success: true}
	if err = s.notificationTick(context.Background(), sender, &scan); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Store.Notifications(1, 20)
	if page.Items[0].Status != "sent" || sender.calls != 2 {
		t.Fatalf("not accepted %+v", page)
	}
	cfg, _ := s.Store.FeishuSettings()
	if cfg.LastSuccessAt == nil || cfg.LastError != "" || cfg.Enabled {
		t.Fatalf("test changed enable or failed %+v", cfg)
	}
	if sender.messages[0].BaseURL != "https://panel.example.com" || sender.messages[0].Events[0].ID != sender.messages[1].Events[0].ID {
		t.Fatal("retry lost stable id")
	}
}

type blockedNotificationSender struct {
	entered chan struct{}
	release chan struct{}
}

func (f *blockedNotificationSender) Send(ctx context.Context, webhook, secret string, m feishu.Message) feishu.Result {
	close(f.entered)
	select {
	case <-f.release:
		return feishu.Result{Success: true}
	case <-ctx.Done():
		return feishu.Result{Retryable: true, Error: "发送已取消"}
	}
}

func TestNotificationNetworkDoesNotHoldDatabaseOrAdmin(t *testing.T) {
	s, _ := newServer(t)
	h := s.AdminHandler()
	cookie, _ := loginAdmin(t, s, h)
	_, err := s.Store.SaveFeishuSettings(model.FeishuSettingsInput{Webhook: notificationTestURL, Secret: "test-signature-secret", Categories: model.DefaultNotificationCategories()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.QueueFeishuTest(); err != nil {
		t.Fatal(err)
	}
	sender := &blockedNotificationSender{make(chan struct{}), make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { var scan time.Time; done <- s.notificationTick(ctx, sender, &scan) }()
	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sender never started")
	}
	response := make(chan int, 1)
	go func() { response <- request(h, "GET", "/api/v1/overview", nil, cookie, "").Code }()
	select {
	case code := <-response:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sending holds database or API")
	}
	close(sender.release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sender never finished")
	}
}

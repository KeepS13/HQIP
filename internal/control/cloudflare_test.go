package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/cloudflare"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

type cfTransport func(*http.Request) (*http.Response, error)

func (f cfTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func cfResponse(v any) *http.Response {
	raw, _ := json.Marshal(map[string]any{"success": true, "result": v})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}
}
func TestCloudflareSettingsRequireAdminAndNeverReturnToken(t *testing.T) {
	s, _ := newServer(t)
	mux := http.NewServeMux()
	s.registerCloudflareRoutes(mux)
	h := s.headers(mux, s.opts.AdminOrigin)
	for _, path := range []string{"/api/v1/settings/cloudflare", "/api/v1/settings/cloudflare/zones", "/api/v1/settings/cloudflare/certificates"} {
		if w := request(h, "GET", path, nil, "", ""); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	if err := s.Store.ChangePassword("changed-password-123"); err != nil {
		t.Fatal(err)
	}
	_, hash, _, err := s.Store.Admin()
	if err != nil {
		t.Fatal(err)
	}
	token, csrf, err := s.Store.CreateSession(hash)
	if err != nil {
		t.Fatal(err)
	}
	cookie := "hqip_session=" + token
	secret := "cloudflare-only-never-return-value"
	zone := cloudflare.Zone{ID: strings.Repeat("1", 32), Name: "example.com", Status: "active"}
	if _, err = s.Store.SaveCloudflareSettings(store.CloudflareSettingsInput{Token: secret, DefaultZoneID: zone.ID, Email: "ops@example.com"}, zone); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/api/v1/settings/cloudflare", nil, cookie, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("unsafe settings response %d %s", w.Code, w.Body.String())
	}
	w = request(h, "PUT", "/api/v1/settings/cloudflare", map[string]any{}, cookie, "")
	if w.Code != 403 {
		t.Fatalf("missing CSRF accepted %d", w.Code)
	}
	w = request(h, "PUT", "/api/v1/settings/cloudflare", map[string]any{"email": "bad"}, cookie, csrf)
	if w.Code != 400 {
		t.Fatalf("invalid email accepted %d", w.Code)
	}
	public := s.PublicHandler()
	if w = request(public, "GET", "/api/v1/settings/cloudflare", nil, cookie, ""); w.Code != 404 {
		t.Fatalf("settings exposed on public listener %d", w.Code)
	}
}
func TestCleanupRecoversUnrecordedCreateWithoutCreatingAnything(t *testing.T) {
	s, _ := newServer(t)
	node, err := s.Store.CreateNode(model.Node{Name: "dns", SSHHost: "203.0.113.2", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.2", Domain: "node.example.com", Port: 443}, "password")
	if err != nil {
		t.Fatal(err)
	}
	record := cloudflare.Record{Type: "TXT", Name: "_acme-challenge.node.example.com", Content: "value", TTL: 120, Comment: "unique HQIP task"}
	intent, err := s.Store.CreateDNSIntent(node.ID, strings.Repeat("1", 32), "example.com", record)
	if err != nil {
		t.Fatal(err)
	}
	record.ID = strings.Repeat("2", 32)
	deletes := 0
	client := cloudflare.NewWithTransport("test", cfTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			t.Fatal("cleanup created a record")
		}
		if r.Method == "DELETE" {
			deletes++
			return cfResponse(map[string]string{"id": record.ID}), nil
		}
		if r.URL.RawQuery != "" {
			return cfResponse([]cloudflare.Record{record}), nil
		}
		return cfResponse(record), nil
	}))
	if err = s.cleanupDNSIntent(context.Background(), client, intent); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatal("recovered record not deleted")
	}
	got, err := s.Store.DNSIntent(intent.ID)
	if err != nil || got.State != "deleted" {
		t.Fatal("cleanup not persisted")
	}
}
func TestCleanupExternalModificationStaysPending(t *testing.T) {
	s, _ := newServer(t)
	node, err := s.Store.CreateNode(model.Node{Name: "dns", SSHHost: "203.0.113.2", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.2", Domain: "node.example.com", Port: 443}, "password")
	if err != nil {
		t.Fatal(err)
	}
	record := cloudflare.Record{Type: "A", Name: node.Domain, Content: node.PublicIP, TTL: 120, Comment: "unique HQIP task", ID: strings.Repeat("2", 32)}
	intent, err := s.Store.CreateDNSIntent(node.ID, strings.Repeat("1", 32), "example.com", record)
	if err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.Proxied = true
	client := cloudflare.NewWithTransport("test", cfTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("changed record was mutated")
		}
		return cfResponse(changed), nil
	}))
	if err = s.cleanupDNSIntent(context.Background(), client, intent); !errors.Is(err, cloudflare.ErrChanged) {
		t.Fatalf("external modification ignored: %v", err)
	}
	got, err := s.Store.DNSIntent(intent.ID)
	if err != nil || got.State != "cleanup" || got.Error == "" {
		t.Fatal("pending cleanup status lost")
	}
}

func TestDeletedNodeDNSCleanupRemainsVisibleAndActiveNodesAreProtected(t *testing.T) {
	s, _ := newServer(t)
	create := func(name string) model.Node {
		n, err := s.Store.CreateNode(model.Node{Name: name, SSHHost: name + ".example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.4", Domain: name + ".example.com", Port: 443}, "password")
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, b, c := create("active"), create("retiring"), create("deleted")
	for _, node := range []model.Node{a, b, c} {
		_, err := s.Store.CreateDNSIntent(node.ID, strings.Repeat("1", 32), "example.com", cloudflare.Record{Type: "A", Name: node.Domain, Content: node.PublicIP, Comment: "owned " + node.ID})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.Store.DB.Exec("UPDATE nodes SET lifecycle='retiring' WHERE id=?", b.ID)
	_, _ = s.Store.DB.Exec("UPDATE nodes SET lifecycle='deleted',deleted_at=1 WHERE id=?", c.ID)
	retired, err := s.Store.DeletedCloudflareNodes()
	if err != nil || len(retired) != 1 || retired[0].ID != c.ID {
		t.Fatalf("tombstone scan %+v %v", retired, err)
	}
	for _, node := range []model.Node{a, b} {
		if err = s.cleanupNodeDNS(context.Background(), node); err == nil || !strings.Contains(err.Error(), "尚未确认停止") {
			t.Fatalf("active/retiring cleanup allowed: %v", err)
		}
	}
	if err = s.cleanupNodeDNS(context.Background(), c); err == nil {
		t.Fatal("missing token hidden")
	}
	statuses, err := s.Store.DNSStatuses()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, status := range statuses {
		if status.NodeID == c.ID {
			found = status.State == "cleanup" && status.Error != "" && status.NodeLifecycle == "deleted"
		}
	}
	if !found {
		t.Fatal("deleted node cleanup failure hidden")
	}
	manual := create("manual")
	_, _ = s.Store.DB.Exec("INSERT INTO certificate_state(node_id,not_after) VALUES(?,?)", manual.ID, 2000000000)
	_, _ = s.Store.DB.Exec("UPDATE nodes SET lifecycle='deleted',deleted_at=1 WHERE id=?", manual.ID)
	if err = s.cleanupNodeDNS(context.Background(), manual); err != nil {
		t.Fatalf("manual cleanup needlessly required token: %v", err)
	}
	if _, err = s.Store.CertificateStatus(manual.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("retired manual certificate state retained")
	}
}

func TestCertificateBackoffRetryAndCancellationDoNotExtendDelay(t *testing.T) {
	s, _ := newServer(t)
	node, err := s.Store.CreateNode(model.Node{Name: "cf-retry", SSHHost: "203.0.113.2", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.2", Domain: "node.example.com", Port: 443, DNSMode: "manual", CertificateMode: "cloudflare"}, "password")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.prepareNodeCloudflare(context.Background(), node); err == nil {
		t.Fatal("missing token accepted")
	}
	before, err := s.Store.CertificateStatus(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err = s.prepareNodeCloudflare(context.Background(), node); !errors.Is(err, errCertificateBackoff) {
			t.Fatalf("unexpected retry: %v", err)
		}
	}
	after, _ := s.Store.CertificateStatus(node.ID)
	if !after.NextAttempt.Equal(before.NextAttempt) {
		t.Fatal("backoff refusal extended retry delay")
	}
	var failures int
	_ = s.Store.DB.QueryRow("SELECT failures FROM certificate_state WHERE node_id=?", node.ID).Scan(&failures)
	if failures != 1 {
		t.Fatal("non-attempt counted as failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = s.prepareNodeCloudflare(ctx, node); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled operation not cancelled: %v", err)
	}
	after, _ = s.Store.CertificateStatus(node.ID)
	if !after.NextAttempt.Equal(before.NextAttempt) {
		t.Fatal("shutdown postponed retry")
	}
}

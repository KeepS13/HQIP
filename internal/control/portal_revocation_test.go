package control

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestPortalSubscriptionRotationRejectsInFlightOldURL(t *testing.T) {
	for _, tc := range []struct{ action, suffix string }{{"rotate", ""}, {"revoke-and-create", ""}, {"rotate", "?format=v2ray"}, {"revoke-and-create", "?format=v2ray"}} {
		t.Run(tc.action+tc.suffix, func(t *testing.T) {
			s, _ := newServer(t)
			u, _, _ := portalLoginFixture(t, s, "subscription@example.com")
			n, err := s.Store.CreateNode(model.Node{Name: "subscription", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Store.SetStatus(n.ID, "ready", "test", false); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Store.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]' WHERE id=?", n.ID); err != nil {
				t.Fatal(err)
			}
			sub := subscriptionFixture(t, s, u, []string{n.ID}, n.ID, 2)
			oldURL, err := s.Store.PortalSubscription(u.ID, sub.ID, "create")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Store.ActivatePortal(); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Store.Rules(); err != nil {
				t.Fatal(err)
			}
			// The first Now call occurs after token resolution but before the
			// profile's revision snapshot. Force rotation into that exact gap.
			entered, release := make(chan struct{}), make(chan struct{})
			var released sync.Once
			t.Cleanup(func() { released.Do(func() { close(release) }) })
			var armed atomic.Bool
			armed.Store(true)
			now := s.Store.Now()
			s.Store.Now = func() time.Time {
				if armed.CompareAndSwap(true, false) {
					close(entered)
					<-release
				}
				return now
			}
			public := s.PublicHandler()
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- request(public, "GET", oldURL+tc.suffix, nil, "", "") }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("subscription request did not reach profile barrier")
			}
			if tc.action == "revoke-and-create" {
				_, err = s.Store.PortalSubscription(u.ID, sub.ID, "revoke")
				if err != nil {
					t.Fatal(err)
				}
			}
			newURL, err := s.Store.PortalSubscription(u.ID, sub.ID, "rotate")
			if err != nil {
				t.Fatal(err)
			}
			released.Do(func() { close(release) })
			select {
			case w := <-result:
				if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "password:") {
					t.Fatalf("old URL obtained replacement credentials: %d", w.Code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("subscription request remained blocked")
			}
			w := request(public, "GET", newURL+tc.suffix, nil, "", "")
			valid := strings.Contains(w.Body.String(), "password:")
			if tc.suffix != "" {
				decoded, decodeErr := base64.StdEncoding.DecodeString(w.Body.String())
				valid = decodeErr == nil && strings.HasPrefix(string(decoded), "hysteria2://")
			}
			if w.Code != 200 || !valid {
				t.Fatalf("replacement subscription unavailable: %d", w.Code)
			}
		})
	}
}

func TestPortalAuthorizationRechecksBrowserAfterMiddleware(t *testing.T) {
	for _, action := range []string{"logout", "admin-reset", "self-password", "disable-reenable", "expired"} {
		t.Run(action, func(t *testing.T) {
			s, _ := newServer(t)
			u, cookie, csrf := portalLoginFixture(t, s, "late-approval@example.com")
			id, _, err := s.Store.BeginPortalAuthorization(strings.Repeat("v", 43), "http://127.0.0.1:41234/callback", strings.Repeat("s", 32), "desktop", "late-approval-installation")
			if err != nil {
				t.Fatal(err)
			}
			_, session, _, err := s.Store.PortalIdentity(strings.TrimPrefix(cookie, "hqip_user="), "browser")
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var released sync.Once
			t.Cleanup(func() { released.Do(func() { close(release) }) })
			mux := http.NewServeMux()
			mux.Handle("POST /api/v1/user/authorize/{id}", s.portalAuth("browser", func(w http.ResponseWriter, r *http.Request, user, current string) {
				close(entered)
				<-release
				s.portalAuthorize(w, r, user, current)
			}))
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				result <- portalRequest(mux, "POST", "/api/v1/user/authorize/"+id, nil, cookie, csrf, s.opts.PublicURL)
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("approval did not pass middleware")
			}
			switch action {
			case "logout":
				err = s.Store.RevokePortalSession(u.ID, session)
			case "disable-reenable":
				if err = s.Store.SetPortalEnabled(u.ID, false); err == nil {
					err = s.Store.SetPortalEnabled(u.ID, true)
				}
			case "expired":
				_, err = s.Store.DB.Exec("UPDATE portal_sessions SET access_expires=0 WHERE id=?", session)
			default:
				err = s.Store.PortalPassword(u.ID, "safe-user-password-123", "replacement-password-123", action == "admin-reset")
			}
			if err != nil {
				t.Fatal(err)
			}
			released.Do(func() { close(release) })
			select {
			case w := <-result:
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("late approval survived %s: %d", action, w.Code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("approval request remained blocked")
			}
			var code string
			if err = s.Store.DB.QueryRow("SELECT code_hash FROM portal_authorizations WHERE id=?", id).Scan(&code); err != nil || code != "" {
				t.Fatal("late approval wrote authorization code")
			}
		})
	}
}

// Authentication pages and their CSP are served by the separate static sites.
func TestAPISurfacesDoNotServeFrontend(t *testing.T) {
	s, _ := newServer(t)
	for _, h := range []http.Handler{s.PublicHandler(), s.AdminHandler()} {
		for _, path := range []string{"/", "/login", "/register", "/authorize?request=test", "/account", "/nodes", "/admin", "/favicon.svg", "/assets/test.js"} {
			w := request(h, "GET", path, nil, "", "")
			if w.Code != 404 || strings.Contains(w.Header().Get("Content-Security-Policy"), "challenges.cloudflare.com") {
				t.Fatalf("frontend exposed by API: %s %d", path, w.Code)
			}
		}
	}
}

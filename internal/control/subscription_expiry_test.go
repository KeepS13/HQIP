package control

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestAdminSubscriptionExpiryAuthorizationValidationAndIsolation(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	u, userCookie, userCSRF := portalLoginFixture(t, s, "expiry-api@example.com")
	sub := subscriptionFixture(t, s, u, nil, "", 2)
	path := fmt.Sprintf("/api/v1/users/%s/subscriptions/%s/expiry", u.ID, sub.ID)
	body := map[string]any{"expiresAt": sub.ExpiresAt.Add(time.Hour).In(time.FixedZone("Beijing", 8*3600)).Format(time.RFC3339), "expectedRevision": sub.Revision}
	for _, tc := range []struct {
		cookie, csrf string
		code         int
	}{{"", "", 401}, {userCookie, userCSRF, 401}, {admin, "", 403}} {
		w := request(h, "PATCH", path, body, tc.cookie, tc.csrf)
		if w.Code != tc.code {
			t.Fatalf("auth: got %d want %d", w.Code, tc.code)
		}
	}
	if w := request(h, "PATCH", strings.Replace(path, u.ID, "wrong-user", 1), body, admin, csrf); w.Code != 404 {
		t.Fatal("wrong owner", w.Code)
	}
	for _, value := range []any{nil, "bad-date", s.Store.Now().Format(time.RFC3339), s.Store.Now().Add(-time.Minute).Format(time.RFC3339)} {
		bad := map[string]any{"expiresAt": value, "expectedRevision": sub.Revision}
		if w := request(h, "PATCH", path, bad, admin, csrf); w.Code != 400 {
			t.Fatal("invalid expiry accepted", value, w.Code)
		}
	}
	w := request(h, "PATCH", path, body, admin, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var updated model.UserSubscription
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil || !updated.ExpiresAt.Equal(sub.ExpiresAt.Add(time.Hour)) {
		t.Fatal("timezone conversion", err)
	}
	if w = request(h, "PATCH", path, body, admin, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "刷新") {
		t.Fatal("stale write", w.Code, w.Body.String())
	}
	// An open form can outlive the grant. Disabled state must not mask expiry.
	if _, err := s.Store.DB.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", s.Store.Now().Add(-time.Second).Unix(), sub.ID); err != nil {
		t.Fatal(err)
	}
	body["expectedRevision"] = updated.Revision
	if w = request(h, "PATCH", path, body, admin, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "已到期") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.Store.DB.Exec("UPDATE portal_subscriptions SET revoked=1 WHERE id=?", sub.ID); err != nil {
		t.Fatal(err)
	}
	if w = request(h, "PATCH", path, body, admin, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "已移除") {
		t.Fatal(w.Code, w.Body.String())
	}
}

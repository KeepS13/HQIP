package control

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func subscriptionFixture(t *testing.T, s *Server, user model.PortalUser, ids []string, defaultID string, slots int) model.UserSubscription {
	t.Helper()
	if len(ids) == 0 {
		n, err := s.Store.CreateNode(model.Node{Name: "subscription fixture", SSHHost: "fixture.example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.17", Domain: store.NewID() + ".example.com", Port: 443}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]' WHERE id=?`, n.ID); err != nil {
			t.Fatal(err)
		}
		ids = []string{n.ID}
		defaultID = n.ID
	}
	p, err := s.Store.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "test subscription", Enabled: true, Listed: true, ValidityDays: 30, NodeIDs: ids, DefaultNodeID: defaultID, MaxConnections: slots}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a pre-upgrade full-pool subscription; export tests may intentionally
	// start with offline nodes. Purchase allocation is tested separately.
	id := store.NewID()
	raw, _ := json.Marshal(ids)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO portal_subscriptions(id,user_id,template_id,created_at,expires,grant_key) VALUES(?,?,?,?,?,?)", []any{id, user.ID, p.ID, s.Store.Now().Unix(), s.Store.Now().Add(30 * 24 * time.Hour).Unix(), store.NewID()}},
		{"INSERT INTO portal_usage(subscription_id) VALUES(?)", []any{id}},
		{"INSERT INTO subscription_allocations(subscription_id,target_count,node_ids) VALUES(?,?,?)", []any{id, len(ids), string(raw)}},
		{"UPDATE portal_users SET revision=revision+1 WHERE id=?", []any{user.ID}},
	} {
		if _, err = s.Store.DB.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := s.Store.UserSubscription(user.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return *sub
}

func TestSubscriptionRequestApprovalAndUserIsolation(t *testing.T) {
	s, h := newServer(t)
	admin, adminCSRF := loginAdmin(t, s, h)
	u, cookie, csrf := portalLoginFixture(t, s, "buyer@example.com")
	other, foreignCookie, foreignCSRF := portalLoginFixture(t, s, "other-buyer@example.com")
	sub := subscriptionFixture(t, s, u, nil, "", 1)
	p, err := s.Store.SubscriptionTemplate(sub.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	pub := s.PublicHandler()
	body := map[string]any{"templateId": p.ID, "templateRevision": p.Revision, "requestId": "repeatable-application-0001"}
	w := portalRequest(pub, "POST", "/api/v1/user/subscription-requests", body, cookie, csrf, s.opts.UserOrigin)
	if w.Code != 410 {
		t.Fatal(w.Code, w.Body.String())
	}
	application, err := s.Store.CreateSubscriptionRequest(u.ID, p.ID, "historical-request-0001", p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	approval := map[string]any{"expectedRevision": application.Revision, "templateRevision": p.Revision, "note": "approved"}
	w = request(h, "POST", "/api/v1/subscription-requests/"+application.ID+"/approve", approval, admin, adminCSRF)
	if w.Code != 410 {
		t.Fatal("retired approval route remained writable", w.Code)
	}
	current, err := s.Store.PortalUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.NodeIDs = append(p.NodeIDs, allocationNode(t, s).ID)
	p, err = s.Store.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Store.UserSubscription(u.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub = *updated
	current, err = s.Store.PortalUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	assignment := map[string]any{"expectedRevision": current.Revision, "templateRevision": p.Revision, "templateId": p.ID, "requestId": "gift-after-payments-0001"}
	for i := 0; i < 2; i++ {
		w = request(h, "POST", "/api/v1/users/"+u.ID+"/subscriptions", assignment, admin, adminCSRF)
		if w.Code != 200 {
			t.Fatal("manual assignment retry", w.Code, w.Body.String())
		}
	}
	subs, err := s.Store.UserSubscriptions(u.ID)
	if err != nil || len(subs) != 2 || subs[0].ID == subs[1].ID {
		t.Fatal("approval did not create exactly one independent instance", err)
	}
	for _, path := range []string{"/api/v1/user/subscriptions/" + sub.ID, "/api/v1/user/subscriptions/" + sub.ID + "/import-link"} {
		method := "GET"
		if len(path) > len("/import-link") && path[len(path)-len("/import-link"):] == "/import-link" {
			method = "POST"
		}
		w = portalRequest(pub, method, path, map[string]any{}, foreignCookie, foreignCSRF, s.opts.UserOrigin)
		if w.Code != 404 {
			t.Fatal("foreign subscription exposed", method, w.Code, w.Body.String())
		}
	}
	foreign, err := s.Store.UserSubscriptions(other.ID)
	if err != nil || len(foreign) != 0 {
		t.Fatal("foreign user received an instance")
	}
	w = request(h, "DELETE", fmt.Sprintf("/api/v1/users/%s/subscriptions/%s", u.ID, sub.ID), map[string]any{"expectedRevision": sub.Revision}, admin, adminCSRF)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	subs, err = s.Store.UserSubscriptions(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, v := range subs {
		if v.Status == "active" {
			active++
		}
	}
	if active != 1 {
		t.Fatal("removal affected the other instance", subs)
	}
}

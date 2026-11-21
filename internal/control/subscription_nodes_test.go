package control

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func TestAdminSubscriptionNodesAuthorizationValidationAndExports(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	u, cookie, userCSRF := portalLoginFixture(t, s, "nodes-api@example.com")
	a, b := allocationNode(t, s), allocationNode(t, s)
	sub := subscriptionFixture(t, s, u, []string{a.ID}, "", 2)
	path := fmt.Sprintf("/api/v1/users/%s/subscriptions/%s/nodes", u.ID, sub.ID)
	body := map[string]any{"nodeIds": []string{b.ID}, "expectedRevision": sub.Revision}
	for _, tc := range []struct {
		cookie, csrf string
		code         int
	}{{"", "", 401}, {cookie, userCSRF, 401}, {admin, "", 403}} {
		w := request(h, "PATCH", path, body, tc.cookie, tc.csrf)
		if w.Code != tc.code {
			t.Fatalf("auth got %d want %d", w.Code, tc.code)
		}
	}
	for _, method := range []string{"GET", "PATCH"} {
		if w := request(h, method, strings.Replace(path, u.ID, "foreign", 1), body, admin, csrf); w.Code != 404 {
			t.Fatal("foreign user", w.Code, w.Body.String())
		}
	}
	if w := request(h, "GET", path, nil, cookie, userCSRF); w.Code != 401 {
		t.Fatal("user candidate access", w.Code)
	}
	for _, bad := range []map[string]any{
		{"expectedRevision": sub.Revision}, {"nodeIds": nil, "expectedRevision": sub.Revision},
		{"nodeIds": []string{}}, {"nodeIds": []string{""}, "expectedRevision": sub.Revision},
		{"nodeIds": []string{b.ID, b.ID}, "expectedRevision": sub.Revision},
		{"nodeIds": []string{}, "expectedRevision": sub.Revision},
		{"nodeIds": []string{a.ID, b.ID}, "expectedRevision": sub.Revision},
	} {
		if w := request(h, "PATCH", path, bad, admin, csrf); w.Code != 400 {
			t.Fatal("invalid body accepted", w.Code, w.Body.String())
		}
	}
	w := request(h, "GET", path, nil, admin, csrf)
	var options store.SubscriptionNodeOptions
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil || w.Code != 200 || len(options.Nodes) != 2 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	for _, n := range options.Nodes {
		if n.ID == b.ID && (n.InTemplate || !n.Selectable) {
			t.Fatal("external candidate", n)
		}
	}
	if err := s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	link, err := s.Store.PortalSubscription(u.ID, sub.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Store.UserSubscription(u.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	body["expectedRevision"] = current.Revision
	w = request(h, "PATCH", path, body, admin, csrf)
	var updated model.UserSubscription
	if err = json.Unmarshal(w.Body.Bytes(), &updated); err != nil || w.Code != 200 || len(updated.NodeIDs) != 1 || updated.NodeIDs[0] != b.ID {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if w = request(h, "PATCH", path, body, admin, csrf); w.Code != 409 {
		t.Fatal("stale write", w.Code)
	}
	for _, format := range []string{"clash", "v2ray", "singbox"} {
		w = request(s.PublicHandler(), "GET", strings.TrimPrefix(link, s.Store.BaseURL)+"?format="+format, nil, "", "")
		text := w.Body.String()
		if format == "v2ray" {
			raw, e := base64.StdEncoding.DecodeString(text)
			if e != nil {
				t.Fatal(e)
			}
			text = string(raw)
		}
		if w.Code != 200 || !strings.Contains(text, b.Domain) || strings.Contains(text, a.Domain) {
			t.Fatal("export did not reflect replacement", format, w.Code, text)
		}
	}
	// An old deletion preview must not authorize deleting a newly assigned node.
	w = request(h, "GET", "/api/v1/nodes/"+b.ID+"/delete-preview", nil, admin, csrf)
	var preview struct {
		UserSubscriptions []store.NodeUserSubscriptionReference `json:"userSubscriptions"`
		Node              model.Node                            `json:"node"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &preview); err != nil || len(preview.UserSubscriptions) != 1 || preview.UserSubscriptions[0].ID != sub.ID {
		t.Fatal("missing delete reference", w.Body.String(), err)
	}
	w = request(h, "DELETE", "/api/v1/nodes/"+b.ID, model.NodeDeleteOptions{ExpectedRevision: preview.Node.Revision}, admin, csrf)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "userSubscriptions") {
		t.Fatal("delete not blocked", w.Code, w.Body.String())
	}
	// Move back to the original node to release the external node's reference;
	// clearing the allocation is no longer an administrative resize operation.
	body["expectedRevision"], body["nodeIds"] = updated.Revision, []string{a.ID}
	if w = request(h, "PATCH", path, body, admin, csrf); w.Code != 200 {
		t.Fatal("replace", w.Code, w.Body.String())
	}
	w = request(h, "DELETE", "/api/v1/nodes/"+b.ID, model.NodeDeleteOptions{ExpectedRevision: preview.Node.Revision}, admin, csrf)
	if w.Code != 202 {
		t.Fatal("cleared reference blocked deletion", w.Code, w.Body.String())
	}
	if _, err = s.Store.DB.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", s.Store.Now().Add(-time.Second).Unix(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if w = request(h, "PATCH", path, body, admin, csrf); w.Code != 409 {
		t.Fatal("expired editable", w.Code)
	}
}

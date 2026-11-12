package control

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func TestNodeDeletionRechecksSubscriptionReferencesAfterPreview(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "protected node", SSHHost: "fixture.example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.29", Domain: "delete.example.com", Port: 443}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]' WHERE id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	previewPath := "/api/v1/nodes/" + n.ID + "/delete-preview"
	if w := request(h, "GET", previewPath, nil, "", ""); w.Code != 401 {
		t.Fatal("anonymous preview exposed", w.Code)
	}
	w := request(h, "GET", previewPath, nil, admin, csrf)
	var preview struct {
		SubscriptionTemplates []store.NodeSubscriptionReference `json:"subscriptionTemplates"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &preview); err != nil || w.Code != 200 || preview.SubscriptionTemplates == nil || len(preview.SubscriptionTemplates) != 0 {
		t.Fatal("expected clear preview", w.Code, w.Body.String(), err)
	}
	p, err := s.Store.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "新引用订阅", Enabled: true, Listed: true, ValidityDays: 30, MaxConnections: 1, NodeIDs: []string{n.ID}, DefaultNodeID: n.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := model.NodeDeleteOptions{ExpectedRevision: n.Revision}
	if w = request(h, "DELETE", "/api/v1/nodes/"+n.ID, body, admin, ""); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	w = request(h, "DELETE", "/api/v1/nodes/"+n.ID, body, admin, csrf)
	var conflict struct {
		Code                  string                            `json:"code"`
		Error                 string                            `json:"error"`
		SubscriptionTemplates []store.NodeSubscriptionReference `json:"subscriptionTemplates"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &conflict); err != nil || w.Code != 409 || conflict.Code != "NODE_REFERENCED_BY_SUBSCRIPTIONS" || len(conflict.SubscriptionTemplates) != 1 || conflict.SubscriptionTemplates[0].ID != p.ID || conflict.SubscriptionTemplates[0].DefaultNode {
		t.Fatal("stale preview bypassed reference protection", w.Code, w.Body.String(), err)
	}
	w = request(h, "GET", previewPath, nil, admin, csrf)
	if err = json.Unmarshal(w.Body.Bytes(), &preview); err != nil || len(preview.SubscriptionTemplates) != 1 {
		t.Fatal("new preview did not explain conflict", w.Body.String(), err)
	}
	p.NodeIDs, p.DefaultNodeID = []string{}, ""
	if _, err = s.Store.SaveSubscriptionTemplate(p.ID, p, p.Revision); err != nil {
		t.Fatal(err)
	}
	w = request(h, "DELETE", "/api/v1/nodes/"+n.ID, body, admin, csrf)
	if w.Code != 202 {
		t.Fatal("explicitly unreferenced node could not be deleted", w.Code, w.Body.String())
	}
}

package control

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestSubscriptionTemplateAPIWithoutManualDefault(t *testing.T) {
	s, handler := newServer(t)
	cookie, csrf := loginAdmin(t, s, handler)
	node := catalogTestNode(t, s, "auto-template", "203.0.113.81")
	body := map[string]any{
		"name": "自动选择测试", "enabled": true, "listed": true,
		"validityDays": 30, "maxConnections": 1, "priceCents": 100,
		"nodeIds": []string{node.ID},
	}
	w := request(handler, "POST", "/api/v1/subscription-templates", body, cookie, csrf)
	var template model.SubscriptionTemplate
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &template) != nil || template.DefaultNodeID != "" {
		t.Fatal("create required a default", w.Code, w.Body.String())
	}
	body["expectedRevision"] = template.Revision
	body["defaultNodeId"] = "obsolete-default-outside-node-list"
	for _, method := range []string{"POST", "PUT"} {
		path := "/api/v1/subscription-templates/" + template.ID
		if method == "POST" {
			path += "/preview"
		}
		w = request(handler, method, path, body, cookie, csrf)
		if w.Code != 200 {
			t.Fatal("legacy default was not ignored", method, w.Code, w.Body.String())
		}
	}
	saved, err := s.Store.SubscriptionTemplate(template.ID)
	if err != nil || saved.DefaultNodeID != "" || len(saved.NodeIDs) != 1 || saved.NodeIDs[0] != node.ID {
		t.Fatal("saved manual default or lost membership", saved, err)
	}
	// Preview and save also accept deliberately clearing the node set.
	body["expectedRevision"] = saved.Revision
	body["nodeIds"] = []string{}
	delete(body, "defaultNodeId")
	w = request(handler, "PUT", "/api/v1/subscription-templates/"+saved.ID, body, cookie, csrf)
	if w.Code != 200 {
		t.Fatal("empty node set rejected", w.Code, w.Body.String())
	}
}

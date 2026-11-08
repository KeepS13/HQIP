package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodeAudienceListIsAdministratorOnlyAndKeepsUnknownOnline(t *testing.T) {
	s, h := newServer(t)
	node, err := s.Store.CreateNode(model.Node{Name: "audience", SSHHost: "audience.example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.10", Domain: "audience.example.com", Port: 443}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/api/v1/nodes", nil, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous node audience visible: %d", w.Code)
	}
	if w := request(s.PublicHandler(), "GET", "/api/v1/nodes", nil, "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("public node audience visible: %d", w.Code)
	}
	cookie, _ := loginAdmin(t, s, h)
	w := request(h, "GET", "/api/v1/nodes", nil, cookie, "")
	var items []model.NodeListItem
	if err = json.Unmarshal(w.Body.Bytes(), &items); w.Code != http.StatusOK || err != nil || len(items) != 1 {
		t.Fatalf("node list response: %d %s %v", w.Code, w.Body.String(), err)
	}
	if items[0].ID != node.ID || items[0].Audience.SubscribedUsers != 0 || items[0].Audience.ActiveSubscriptions != 0 || items[0].Audience.OnlineUsers != nil || items[0].Audience.OnlineConnections != nil {
		t.Fatalf("unexpected audience: %+v", items[0])
	}
}

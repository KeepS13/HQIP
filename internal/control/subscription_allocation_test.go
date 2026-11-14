package control

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func allocationNode(t *testing.T, s *Server) model.Node {
	t.Helper()
	id := store.NewID()
	n, err := s.Store.CreateNode(model.Node{Name: "allocation-" + id, SSHHost: id + ".example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.18", Domain: id + ".example.com", Port: 443}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetStatus(n.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]',last_seen=?,core_healthy=1 WHERE id=?`, s.Store.Now().Unix(), n.ID); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSubscriptionAllocationCatalogAndExports(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	u, cookie, _ := portalLoginFixture(t, s, "allocation-ui@example.com")
	ids := []string{}
	for i := 0; i < 6; i++ {
		ids = append(ids, allocationNode(t, s).ID)
	}
	body := map[string]any{"name": "介绍测试", "description": "每行一条\n<script>纯文本</script>", "nodesPerSubscription": 2, "nodeIds": ids, "maxConnections": 2, "validityDays": 7, "enabled": true, "listed": true, "priceCents": 300}
	w := request(h, "POST", "/api/v1/subscription-templates", body, admin, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p model.SubscriptionTemplate
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Description != body["description"] || p.NodesPerSubscription != 2 {
		t.Fatal(p)
	}
	pub := s.PublicHandler()
	if err := s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		w = request(pub, "GET", "/api/v1/user/catalog", nil, cookie, "")
		var catalog []model.CatalogSubscription
		if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || w.Code != 200 || len(catalog) != 1 || catalog[0].AvailableCopies != 3-i {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		e, err := s.Store.AssignSubscription(u.ID, p.ID, fmt.Sprintf("allocation-copy-%d", i), -1, p.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if i != 0 {
			continue
		}
		link, err := s.Store.PortalSubscription(u.ID, e.ID, "create")
		if err != nil {
			t.Fatal(err)
		}
		path := strings.TrimPrefix(link, s.Store.BaseURL)
		for _, format := range []string{"clash", "v2ray", "singbox"} {
			w = request(pub, "GET", path+"?format="+format, nil, "", "")
			if w.Code != 200 {
				t.Fatal(format, w.Code, w.Body.String())
			}
			text := w.Body.String()
			if format == "v2ray" {
				decoded, err := base64.StdEncoding.DecodeString(text)
				if err != nil {
					t.Fatal(err)
				}
				text = string(decoded)
			}
			for _, id := range ids {
				allocated := id == e.NodeIDs[0] || id == e.NodeIDs[1]
				if strings.Contains(text, id) != allocated {
					t.Fatal("export allocation mismatch", format, id, allocated)
				}
			}
		}
		for _, id := range ids {
			if id == e.NodeIDs[0] || id == e.NodeIDs[1] {
				continue
			}
			if _, err = s.Store.PortalCredential(u.ID, e.ID, id, "subscription"); err == nil {
				t.Fatal("unallocated credential issued")
			}
		}
	}
	w = request(pub, "GET", "/api/v1/user/catalog", nil, cookie, "")
	var catalog []model.CatalogSubscription
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || catalog[0].AvailableCopies != 0 || catalog[0].AvailabilityReason == "" {
		t.Fatal(w.Body.String(), err)
	}
	w = request(h, "POST", "/api/v1/users/"+u.ID+"/subscriptions", map[string]any{"templateId": p.ID, "templateRevision": p.Revision, "requestId": "fourth-allocation", "expectedRevision": -1}, admin, csrf)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "库存") {
		t.Fatal(w.Code, w.Body.String())
	}
}

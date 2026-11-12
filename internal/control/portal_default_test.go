package control

import (
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
)

func TestPortalSubscriptionDefaultOrderAndUnavailableRequiresSelection(t *testing.T) {
	s, _ := newServer(t)
	u, _, _ := portalLoginFixture(t, s, "default-order@example.com")
	nodes := []model.Node{}
	for _, name := range []string{"first", "default"} {
		n, err := s.Store.CreateNode(model.Node{Name: name, SSHHost: name + ".example.com", Domain: name + ".example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Store.SetStatus(n.ID, "ready", "test", false); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Store.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]',last_seen=?,core_healthy=1 WHERE id=?", s.Store.Now().Unix(), n.ID); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	// Prefer the unlimited server even though the stored legacy default is first.
	if _, err := s.Store.DB.Exec("UPDATE servers SET config=json_set(config,'$.monthlyQuotaBytes',1000) WHERE id=?", nodes[0].ServerID); err != nil {
		t.Fatal(err)
	}
	sub := subscriptionFixture(t, s, u, []string{nodes[0].ID, nodes[1].ID}, nodes[0].ID, 1)
	link, err := s.Store.PortalSubscription(u.ID, sub.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	for _, preferredReady := range []bool{true, false} {
		wantNode := nodes[1]
		if !preferredReady {
			if err = s.Store.SetStatus(nodes[1].ID, "pending_verification", "test", false); err != nil {
				t.Fatal(err)
			}
			wantNode = nodes[0]
		}
		w := request(s.PublicHandler(), "GET", link, nil, "", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var config struct {
			Proxies []struct{ Name string } `yaml:"proxies"`
			Groups  []struct {
				Name    string
				Type    string
				Proxies []string
			} `yaml:"proxy-groups"`
		}
		if err = yaml.Unmarshal(w.Body.Bytes(), &config); err != nil {
			t.Fatal(err)
		}
		want := wantNode.Name + " · " + wantNode.ID
		if len(config.Proxies) == 0 || len(config.Groups) != 1 || config.Proxies[0].Name != want || config.Groups[0].Proxies[0] != want || config.Groups[0].Type != "select" {
			t.Fatal("export did not use service-selected node first")
		}
	}
	if _, err = s.Store.DB.Exec("UPDATE nodes SET last_seen=0"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?format=v2ray", "?format=singbox"} {
		w := request(s.PublicHandler(), "GET", link+suffix, nil, "", "")
		if w.Code != 503 || !strings.Contains(w.Body.String(), "暂无可用节点") || strings.Contains(w.Body.String(), "password") {
			t.Fatal("empty automatic selection exported an arbitrary node", w.Code)
		}
	}
}

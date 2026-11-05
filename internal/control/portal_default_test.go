package control

import (
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
		if _, err = s.Store.DB.Exec("UPDATE nodes SET capabilities='[\"admission-v1\"]' WHERE id=?", n.ID); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	sub := subscriptionFixture(t, s, u, []string{nodes[0].ID, nodes[1].ID}, nodes[1].ID, 1)
	link, err := s.Store.PortalSubscription(u.ID, sub.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	for _, defaultReady := range []bool{true, false} {
		if !defaultReady {
			if err = s.Store.SetStatus(nodes[1].ID, "pending_verification", "test", false); err != nil {
				t.Fatal(err)
			}
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
		if defaultReady {
			want := nodes[1].Name + " · " + nodes[1].ID
			if len(config.Proxies) != 2 || len(config.Groups) != 1 || config.Proxies[0].Name != want || config.Groups[0].Proxies[0] != want {
				t.Fatal("subscription did not select plan default first")
			}
		} else if len(config.Proxies) != 1 || len(config.Groups) != 2 || config.Groups[0].Proxies[0] != "默认节点不可用，请手动选择" || config.Groups[1].Name != config.Groups[0].Proxies[0] || len(config.Groups[1].Proxies) != 1 || config.Groups[1].Proxies[0] != "REJECT" || len(config.Groups[0].Proxies) != 2 {
			t.Fatal("unavailable default silently selected a different node")
		}
		for _, group := range config.Groups {
			if group.Type != "select" {
				t.Fatal("subscription added automatic switching")
			}
		}
	}
}

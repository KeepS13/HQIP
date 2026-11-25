package control

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestHostNetworkAdministratorContractAndUnenrichedNode(t *testing.T) {
	s, h := newServer(t)
	node, err := s.Store.CreateNode(model.Node{Name: "network", SSHHost: "network.example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.11", Domain: "network.example.com", Port: 443}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	at := s.Store.Now().UTC()
	sample := model.HostNetworkSample{BootID: "11111111-1111-1111-1111-111111111111", BootAt: at.Add(-time.Hour), At: at, Interfaces: []model.HostNetworkInterface{{Name: "eth0", Index: 2, Receive: 123, Send: 456}}}
	if err = s.Store.Report(node.ID, model.AgentReport{Revision: 1, HostNetwork: []model.HostNetworkSample{sample}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/nodes", "/api/v1/nodes/" + node.ID, "/api/v1/servers/" + node.ServerID} {
		if w := request(h, "GET", path, nil, "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous host data exposed at %s: %d", path, w.Code)
		}
		if w := request(s.PublicHandler(), "GET", path, nil, "", ""); w.Code != http.StatusNotFound {
			t.Fatalf("public host data exposed at %s: %d", path, w.Code)
		}
	}
	cookie, _ := loginAdmin(t, s, h)
	var list []model.NodeListItem
	w := request(h, "GET", "/api/v1/nodes", nil, cookie, "")
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list) != 1 || list[0].HostNetwork == nil || list[0].HostNetwork.Send != 456 {
		t.Fatalf("host list: %d %s %v", w.Code, w.Body.String(), err)
	}
	var detail struct {
		Node model.Node `json:"node"`
	}
	w = request(h, "GET", "/api/v1/nodes/"+node.ID, nil, cookie, "")
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || detail.Node.HostNetwork == nil || detail.Node.HostNetwork.Receive != 123 {
		t.Fatalf("host detail: %d %s %v", w.Code, w.Body.String(), err)
	}
	plain, err := s.Store.Node(node.ID)
	if err != nil || plain.HostNetwork != nil || plain.TotalUpload != 0 || plain.TotalDownload != 0 {
		t.Fatal("admin host enrichment leaked into generic node or proxy totals", plain, err)
	}
}

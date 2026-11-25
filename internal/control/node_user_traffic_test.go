package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodeUserTrafficAdministratorContract(t *testing.T) {
	s, h := newServer(t)
	node, err := s.Store.CreateNode(model.Node{Name: "ranking", SSHHost: "ranking.example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.11", Domain: "ranking.example.com", Port: 443}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/nodes/" + node.ID + "/user-traffic"
	if w := request(h, "GET", path, nil, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous ranking exposed: %d", w.Code)
	}
	cookie, _ := loginAdmin(t, s, h)
	if w := request(s.PublicHandler(), "GET", path, nil, cookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("public route exposed: %d", w.Code)
	}
	for _, suffix := range []string{"", "?offset=20&limit=5"} {
		w := request(h, "GET", path+suffix, nil, cookie, "")
		var page model.NodeUserTrafficPage
		if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.Items == nil || len(page.Items) != 0 || page.NodeID != node.ID || page.TotalUsers != 0 || page.GeneratedAt.IsZero() {
			t.Fatalf("empty page contract: %d %s %v", w.Code, w.Body.String(), err)
		}
		if suffix == "" && (page.Offset != 0 || page.Limit != 20) || suffix != "" && (page.Offset != 20 || page.Limit != 5) {
			t.Fatalf("paging defaults: %+v", page)
		}
	}
	for _, query := range []string{"offset=-1", "offset=1000001", "limit=0", "limit=101", "offset=abc", "limit=1.5", "limit=", "offset=", "offset=9999999999999999999999999", "limit=1&limit=2"} {
		if w := request(h, "GET", path+"?"+query, nil, cookie, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid paging %s: %d %s", query, w.Code, w.Body.String())
		}
	}
	if w := request(h, "GET", "/api/v1/nodes/does-not-exist/user-traffic", nil, cookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing node: %d %s", w.Code, w.Body.String())
	}
}

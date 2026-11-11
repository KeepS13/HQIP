package control

import (
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestQualityImageOnlyAdminPrivateAndVersionBound(t *testing.T) {
	s, h := newServer(t)
	cookie, _ := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "image-node", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.DB.Exec("INSERT INTO node_network_quality(node_id,status,job_id,source_ip,requested_at,report_at,report_ip,report) VALUES(?,'complete',0,?,?,?,?,?)", n.ID, n.PublicIP, s.Store.Now().Unix(), s.Store.Now().Unix(), n.PublicIP, `{"Head":{"IP":"203.0.113.8"}}`)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Store.NetworkQuality(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/nodes/" + n.ID + "/network-quality/image?version=" + v.ReportImageVersion
	if w := request(s.PublicHandler(), "GET", path, nil, "", ""); w.Code != 404 {
		t.Fatal("public image exposed", w.Code)
	}
	if w := request(h, "GET", path, nil, "", ""); w.Code != 401 {
		t.Fatal("unauthenticated image exposed", w.Code)
	}
	w := request(h, "GET", path, nil, cookie, "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatal(w.Code, w.Body.String())
	}
	for name, want := range map[string]string{"Cache-Control": "private, no-store", "X-Content-Type-Options": "nosniff", "Content-Security-Policy": "default-src 'none'; style-src 'none'; sandbox"} {
		if w.Header().Get(name) != want {
			t.Fatalf("%s: %s", name, w.Header().Get(name))
		}
	}
	if w = request(h, "GET", strings.Replace(path, v.ReportImageVersion, strings.Repeat("0", 64), 1), nil, cookie, ""); w.Code != 409 {
		t.Fatal("superseded image did not conflict", w.Code)
	}
	if w = request(h, "GET", strings.Replace(path, v.ReportImageVersion, "invalid", 1), nil, cookie, ""); w.Code != 400 {
		t.Fatal("invalid version accepted", w.Code)
	}
	status := request(h, "GET", "/api/v1/nodes/"+n.ID+"/network-quality", nil, cookie, "")
	if status.Code != 200 || strings.Contains(status.Body.String(), "<svg") || strings.Contains(status.Body.String(), "_hqipImage") {
		t.Fatal("status exposed image bytes", status.Body.String())
	}
}

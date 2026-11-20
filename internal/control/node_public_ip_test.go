package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodePublicIPEnrollmentAPI(t *testing.T) {
	s, h := newServer(t)
	installer := &monitoringInstaller{}
	s.runner = installer
	cookie, csrf := loginAdmin(t, s, h)
	serverForm := map[string]any{"name": "manual-ip", "sshHost": " 8.8.8.8 ", "password": "enrollment-secret", "saveCredential": true, "expiresAt": s.Store.Now().Add(24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)}
	w := request(h, "POST", "/api/v1/servers", serverForm, cookie, csrf)
	if w.Code != 201 {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	var server model.Server
	if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	if server.SSHHost != "8.8.8.8" || len(server.Addresses) != 0 {
		t.Fatal("enrollment must not deploy or register an address", server)
	}
	body := map[string]any{"serverId": server.ID, "publicIp": " 8.8.4.4 ", "name": "first", "domain": "first.example.com", "port": 443}
	if w = request(h, "POST", "/api/v1/nodes", body, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if w = request(s.PublicHandler(), "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 404 {
		t.Fatal("node enrollment exposed publicly")
	}
	w = request(h, "POST", "/api/v1/nodes", body, cookie, csrf)
	if w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var node model.Node
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.PublicIP != "8.8.4.4" || node.AddressID == "" || node.SSHHost != server.SSHHost || strings.Contains(w.Body.String(), "enrollment-secret") {
		t.Fatalf("bad node: %+v", node)
	}
	if w = request(h, "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "该公网 IP 已有节点") {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body.String())
	}
	addresses, err := s.Store.Addresses(server.ID)
	if err != nil || len(addresses) != 1 || addresses[0].ID != node.AddressID {
		t.Fatalf("address registration: %+v %v", addresses, err)
	}

	// Old clients can still register an address first and submit its ID.
	a, err := s.Store.CreateAddress(server.ID, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	body["addressId"], body["domain"] = a.ID, "second.example.com"
	body["publicIp"] = "9.9.9.9"
	if w = request(h, "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 400 {
		t.Fatal("mismatched ID/IP accepted", w.Body.String())
	}
	body["addressId"] = "foreign-address"
	if w = request(h, "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 400 {
		t.Fatal("foreign address accepted")
	}
	body["addressId"] = a.ID
	delete(body, "publicIp")
	if w = request(h, "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 202 {
		t.Fatalf("legacy ID: %d %s", w.Code, w.Body.String())
	}

	// Reuse an unused address with the new publicIp contract.
	a, err = s.Store.CreateAddress(server.ID, "9.9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	delete(body, "addressId")
	body["publicIp"], body["domain"] = a.IP, "third.example.com"
	w = request(h, "POST", "/api/v1/nodes", body, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil || node.AddressID != a.ID {
		t.Fatalf("did not reuse address: %+v %v", node, err)
	}
	// Duplicate-domain failure must roll back its newly inserted address.
	body["publicIp"] = "1.0.0.1"
	if w = request(h, "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 409 {
		t.Fatal("duplicate domain accepted")
	}
	addresses, err = s.Store.Addresses(server.ID)
	if err != nil || len(addresses) != 3 {
		t.Fatalf("failed node leaked address: %+v %v", addresses, err)
	}
}

func TestPublicIPv4APICannotBypassValidation(t *testing.T) {
	s, h := newServer(t)
	installer := &monitoringInstaller{}
	s.runner = installer
	cookie, csrf := loginAdmin(t, s, h)
	v, err := s.Store.CreateServer(model.Server{Name: "validation", SSHHost: "8.8.8.8"})
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{"vps.example.com", "::1", "::ffff:8.8.8.8", "10.0.0.1", "127.0.0.1", "100.64.0.1", "169.254.1.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "1.2.3.256", "01.2.3.4", "8.8.8.8:22", "8.8.8.8/32"}
	for _, ip := range invalid {
		serverForm := map[string]any{"name": "invalid", "sshHost": ip, "password": "secret", "expiresAt": s.Store.Now().Add(24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)}
		if w := request(h, "POST", "/api/v1/servers", serverForm, cookie, csrf); w.Code != 400 {
			t.Fatalf("server %q: %d %s", ip, w.Code, w.Body.String())
		}
		delete(serverForm, "expiresAt")
		serverForm["expectedRevision"] = v.Revision
		if w := request(h, "PATCH", "/api/v1/servers/"+v.ID, serverForm, cookie, csrf); w.Code != 400 {
			t.Fatalf("update accepted %q", ip)
		}
		body := map[string]any{"serverId": v.ID, "publicIp": ip, "name": "invalid", "domain": "invalid.example.com", "password": "secret"}
		if w := request(h, "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 400 {
			t.Fatalf("node %q: %d %s", ip, w.Code, w.Body.String())
		}
		if w := request(h, "POST", "/api/v1/servers/"+v.ID+"/addresses", map[string]string{"publicIp": ip}, cookie, csrf); w.Code < 400 {
			t.Fatalf("address accepted %q", ip)
		}
	}
	if installer.checks != 0 {
		t.Fatal("invalid address reached SSH")
	}
	// Missing saved credentials must fail before creating either address or job.
	body := map[string]any{"serverId": v.ID, "publicIp": "1.1.1.1", "name": "missing-secret", "domain": "missing.example.com"}
	if w := request(h, "POST", "/api/v1/nodes", body, cookie, csrf); w.Code != 409 {
		t.Fatalf("missing credential: %d %s", w.Code, w.Body.String())
	}
	for _, table := range []string{"server_addresses", "nodes", "jobs"} {
		var count int
		if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s changed before validation: %d %v", table, count, err)
		}
	}
}

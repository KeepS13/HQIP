package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/provision"
)

type managedInstaller struct {
	fakeInstaller
	machine  string
	takeover bool
	block    chan struct{}
}

func (i *managedInstaller) CheckServer(ctx context.Context, s model.Server, p string, pin provision.PinFunc) (provision.ServerInspection, error) {
	if p == "wrong" {
		return provision.ServerInspection{}, errors.New("authentication failed")
	}
	if err := pin("SHA256:fixed-test-host-key"); err != nil {
		return provision.ServerInspection{}, err
	}
	machine := i.machine
	if machine == "" {
		machine = "machine-a"
	}
	return provision.ServerInspection{MachineID: machine, Platform: "ubuntu 24.04", Architecture: "x86_64", BoundIPs: []string{"203.0.113.10", "203.0.113.11"}}, nil
}
func (i *managedInstaller) Inspect(ctx context.Context, in provision.Input, log provision.LogFunc, pin provision.PinFunc) (provision.PreflightResult, error) {
	return provision.PreflightResult{Fingerprint: "verified-snapshot", CanTakeover: i.takeover, CanInstall: !i.takeover, ServerInspection: provision.ServerInspection{MachineID: "machine-a", BoundIPs: []string{in.Node.PublicIP}}}, nil
}
func (i *managedInstaller) Install(ctx context.Context, in provision.Input, log provision.LogFunc, pin provision.PinFunc) (provision.Result, error) {
	if i.block != nil {
		close(i.block)
		<-ctx.Done()
		return provision.Result{Managed: true}, ctx.Err()
	}
	return provision.Result{Verified: true, Managed: true, MachineID: "machine-a"}, nil
}
func (i *managedInstaller) Retire(ctx context.Context, in provision.Input, log provision.LogFunc, pin provision.PinFunc) (provision.Result, error) {
	return provision.Result{}, nil
}

func TestServerEnrollmentNodeOwnershipAndDeletionAPI(t *testing.T) {
	s, h := newServer(t)
	i := &managedInstaller{}
	s.runner = i
	cookie, csrf := loginAdmin(t, s, h)
	body := map[string]any{"name": "VPS", "sshHost": "8.8.8.10", "sshPort": 22, "sshUser": "root", "password": "ephemeral-secret"}
	body["expiresAt"] = s.Store.Now().Add(30 * 24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)
	if w := request(h, "POST", "/api/v1/servers", body, cookie, ""); w.Code != 403 {
		t.Fatalf("missing csrf: %d", w.Code)
	}
	w := request(h, "POST", "/api/v1/servers", body, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "ephemeral-secret") {
		t.Fatal("SSH secret returned")
	}
	var server model.Server
	json.Unmarshal(w.Body.Bytes(), &server)
	w = request(h, "POST", "/api/v1/servers/"+server.ID+"/addresses", map[string]string{"publicIp": "8.8.8.11"}, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var address model.ServerAddress
	json.Unmarshal(w.Body.Bytes(), &address)
	f := map[string]any{"serverId": server.ID, "addressId": address.ID, "name": "IP B", "domain": "b.example.com", "password": "install-secret", "port": 443}
	w = request(h, "POST", "/api/v1/nodes", f, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var node model.Node
	json.Unmarshal(w.Body.Bytes(), &node)
	if node.PublicIP != address.IP || node.SSHHost != server.SSHHost {
		t.Fatalf("incorrect source of node address: %+v", node)
	}
	f["addressId"] = "foreign-address"
	w = request(h, "POST", "/api/v1/nodes", f, cookie, csrf)
	if w.Code != 400 {
		t.Fatal("accepted foreign address")
	}
	i.machine = "changed-machine"
	delete(body, "expiresAt")
	body["expectedRevision"] = server.Revision
	w = request(h, "PATCH", "/api/v1/servers/"+server.ID, body, cookie, csrf)
	if w.Code != 400 {
		t.Fatal("accepted changed machine identity")
	}
	w = request(h, "DELETE", "/api/v1/servers/"+server.ID+"/addresses/"+address.ID, nil, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("removed deployed IP")
	}
	w = request(h, "DELETE", "/api/v1/nodes/"+node.ID, map[string]any{"expectedRevision": node.Revision, "replacements": map[string]any{}}, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	w = request(h, "DELETE", "/api/v1/servers/"+server.ID+"/addresses/"+address.ID, nil, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/api/v1/servers", "/api/v1/servers/" + server.ID, "/api/v1/nodes/" + node.ID + "/delete-preview"} {
		if w := request(s.PublicHandler(), http.MethodGet, path, nil, cookie, csrf); w.Code != 404 {
			t.Fatalf("public admin route %s=%d", path, w.Code)
		}
	}
}

func TestPreflightRequiredForTakeoverAndDeletionCancelsInstaller(t *testing.T) {
	s, h := newServer(t)
	i := &managedInstaller{takeover: true}
	s.runner = i
	cookie, csrf := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "legacy", SSHHost: "203.0.113.20", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.20", Domain: "legacy.example.com", Port: 443}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.Store.FinishJob(job, false)
	path := "/api/v1/nodes/" + n.ID
	w := request(h, "POST", path+"/takeover", map[string]any{"password": "secret", "preflightFingerprint": "made-up"}, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("accepted unverified snapshot")
	}
	w = request(h, "POST", path+"/preflight", map[string]string{"password": "secret"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(h, "POST", path+"/takeover", map[string]any{"password": "secret", "preflightFingerprint": "verified-snapshot"}, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	job, err = s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if !job.Takeover || job.PreflightFingerprint != "verified-snapshot" {
		t.Fatal("takeover approval lost")
	}
	i.block = make(chan struct{})
	done := make(chan struct{})
	go func() { s.runJob(context.Background(), job); close(done) }()
	<-i.block
	n, _ = s.Store.Node(n.ID)
	_, err = s.Store.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("installer not cancelled")
	}
	n, _ = s.Store.Node(n.ID)
	if n.Lifecycle != "retiring" || !n.Managed || n.Status == "ready" {
		t.Fatalf("partial owner lost or resurrected: %+v", n)
	}
}

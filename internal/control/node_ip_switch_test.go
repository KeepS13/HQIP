package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/provision"
)

type ipSwitchInstaller struct {
	managedInstaller
	fail bool
	last provision.Input
}

func (i *ipSwitchInstaller) Install(ctx context.Context, in provision.Input, log provision.LogFunc, pin provision.PinFunc) (provision.Result, error) {
	i.last = in
	if i.fail {
		return provision.Result{Managed: true}, errors.New("target IP unavailable")
	}
	return i.managedInstaller.Install(ctx, in, log, pin)
}

func TestNodeIPSwitchAPIConfirmationAndWorkerRetry(t *testing.T) {
	s, h := newServer(t)
	installer := &ipSwitchInstaller{}
	s.runner = installer
	cookie, csrf := loginAdmin(t, s, h)
	w := request(h, "POST", "/api/v1/servers", map[string]any{"name": "switch", "sshHost": "8.8.8.8", "password": "saved-secret", "saveCredential": true, "expiresAt": s.Store.Now().Add(24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)}, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var server model.Server
	if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	w = request(h, "POST", "/api/v1/nodes", map[string]any{"serverId": server.ID, "publicIp": server.SSHHost, "name": "keep this node", "domain": "switch.example.com", "port": 443}, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var node model.Node
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	j, err := s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.runJob(context.Background(), j)
	w = request(h, "PATCH", "/api/v1/servers/"+server.ID, map[string]any{"name": server.Name, "sshHost": "1.1.1.1", "expectedRevision": server.Revision}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	before, err := s.Store.Node(node.ID)
	if err != nil || before.PublicIP != node.PublicIP || before.SSHHost != server.SSHHost || before.IPSwitch != nil {
		t.Fatal("SSH edit changed node IP before consent", err)
	}
	path := "/api/v1/nodes/" + node.ID + "/public-ip"
	body := map[string]any{"oldPublicIp": node.PublicIP, "publicIp": server.SSHHost, "expectedRevision": before.Revision, "expectedServerRevision": server.Revision, "manualDnsConfirmed": true}
	if w = request(h, "POST", path, body, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if w = request(s.PublicHandler(), "POST", path, body, cookie, csrf); w.Code != 404 {
		t.Fatal("switch endpoint exposed publicly")
	}
	body["manualDnsConfirmed"] = false
	if w = request(h, "POST", path, body, cookie, csrf); w.Code != 409 {
		t.Fatal("manual DNS not confirmed", w.Body.String())
	}
	body["manualDnsConfirmed"] = true
	w = request(h, "POST", path, body, cookie, csrf)
	if w.Code != 202 || strings.Contains(w.Body.String(), "saved-secret") {
		t.Fatal("switch failed or leaked secret", w.Code)
	}
	if w = request(h, "POST", path, body, cookie, csrf); w.Code != 409 {
		t.Fatal("duplicate switch accepted")
	}
	j, err = s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	installer.fail = true
	s.runJob(context.Background(), j)
	failed, err := s.Store.Node(node.ID)
	if err != nil || failed.Status != "error" || failed.IPSwitch == nil {
		t.Fatal("failed switch marked complete", failed, err)
	}
	if installer.last.Node.PublicIP != server.SSHHost || installer.last.Node.IPSwitch.FromIP != node.PublicIP || installer.last.Password != "saved-secret" {
		t.Fatal("worker lost switch intent or saved password")
	}
	w = request(h, "POST", "/api/v1/nodes/"+node.ID+"/retry", map[string]string{}, cookie, csrf)
	if w.Code != 202 {
		t.Fatal("retry failed", w.Body.String())
	}
	j, err = s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	installer.fail = false
	s.runJob(context.Background(), j)
	done, err := s.Store.Node(node.ID)
	if err != nil || done.Status != "ready" || done.IPSwitch != nil || done.PublicIP != server.SSHHost || done.ID != node.ID || done.Domain != node.Domain {
		t.Fatal("switch did not finish on same node", done, err)
	}
}

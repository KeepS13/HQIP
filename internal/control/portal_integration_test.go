package control

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

// This test-only writer creates an isolated controller database and private
// native-session fixture. There is no production registration bypass endpoint.
// It refuses existing directories and never connects, deploys or starts nodes.
func TestWritePortalIntegrationFixture(t *testing.T) {
	output := os.Getenv("HQIP_PORTAL_FIXTURE_OUTPUT")
	if output == "" {
		t.Skip("explicit isolated fixture output not requested")
	}
	abs, err := filepath.Abs(output)
	if err != nil || !strings.Contains(strings.ToLower(filepath.Base(abs)), "fixture") {
		t.Fatal("fixture output must be a new directory whose name contains fixture")
	}
	if err = os.Mkdir(abs, 0700); err != nil {
		t.Fatal("refusing existing/inaccessible fixture directory", err)
	}
	base := os.Getenv("HQIP_PORTAL_FIXTURE_PUBLIC_URL")
	if base == "" {
		base = "http://127.0.0.1:18491"
	}
	publicIP := os.Getenv("HQIP_PORTAL_FIXTURE_NODE_IP")
	if publicIP == "" {
		publicIP = "127.0.0.1"
	}
	domain := os.Getenv("HQIP_PORTAL_FIXTURE_DOMAIN")
	if domain == "" {
		domain = "fixture.example.invalid"
	}
	dataDir := filepath.Join(abs, "data")
	db, err := store.Open(dataDir, base)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adminPassword := secure.Token()
	if err = db.InitAdmin("fixture-admin", adminPassword); err != nil {
		t.Fatal(err)
	}
	if err = db.ChangePassword(adminPassword); err != nil {
		t.Fatal(err)
	}
	nodeInfo := []map[string]any{}
	ids := []string{}
	for i, name := range []string{"fixture-a", "fixture-b"} {
		n, err := db.CreateNode(model.Node{Name: name, SSHHost: publicIP, SSHPort: 22, SSHUser: "root", PublicIP: publicIP, Domain: name + ".example.invalid", Port: 9443 + i}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.DB.Exec("UPDATE nodes SET config=json_set(config,'$.domain',?,'$.status','ready','$.message','isolated integration fixture'),capabilities='[\"admission-v1\"]' WHERE id=?", domain, n.ID); err != nil {
			t.Fatal(err)
		}
		_, agent, probe, err := db.NodeSecrets(n.ID)
		if err != nil {
			t.Fatal(err)
		}
		nodeInfo = append(nodeInfo, map[string]any{"id": n.ID, "port": 9443 + i, "publicIp": publicIP, "domain": domain, "agentToken": agent, "probePassword": probe, "controlUrl": base})
		ids = append(ids, n.ID)
	}
	if _, err = db.DB.Exec("UPDATE jobs SET state='done'"); err != nil {
		t.Fatal(err)
	}
	email := "fixture-user@example.invalid"
	password := secure.Token()
	u, err := db.RegisterPortal(email, password)
	if err != nil {
		t.Fatal(err)
	}
	bandwidth := float64(8)
	quota := int64(128 << 20)
	p, err := db.SavePlan("", model.Plan{Name: "isolated acceptance", Enabled: true, SaleEnabled: true, NodeIDs: ids, DefaultNodeID: ids[0], BandwidthMbps: &bandwidth, QuotaBytes: &quota, MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AssignPortal(u.ID, p.ID, time.Now().Add(24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PortalSubscription(u.ID, "create"); err != nil {
		t.Fatal(err)
	}
	if err = db.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	native := []model.PortalTokens{}
	browser, _, err := db.PortalLogin(email, password)
	if err != nil {
		t.Fatal(err)
	}
	for _, installation := range []string{"fixture-installation-a", "fixture-installation-b"} {
		verifier := secure.Token()
		sum := sha256.Sum256([]byte(verifier))
		redirect := "http://127.0.0.1:45678/callback"
		id, _, err := db.BeginPortalAuthorization(base64.RawURLEncoding.EncodeToString(sum[:]), redirect, secure.Token(), "fixture desktop", installation)
		if err != nil {
			t.Fatal(err)
		}
		callback, err := db.ApprovePortalAuthorization(id, u.ID, browser.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		urlValue, _ := url.Parse(callback)
		tokens, err := db.ExchangePortalCode(urlValue.Query().Get("code"), verifier, redirect, installation)
		if err != nil {
			t.Fatal(err)
		}
		native = append(native, tokens)
	}
	fixture := map[string]any{"dataDir": dataDir, "publicUrl": base, "admin": map[string]string{"username": "fixture-admin", "password": adminPassword}, "user": map[string]any{"id": u.ID, "email": email, "password": password}, "planId": p.ID, "nodes": nodeInfo, "nativeSessions": native}
	raw, _ := json.MarshalIndent(fixture, "", "  ")
	if err = os.WriteFile(filepath.Join(abs, "private-fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated fixture created; private tokens written to output directory, no services started")
}

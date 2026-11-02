package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/rules"
	"github.com/KeepS13/HQIP/internal/secure"
)

func TestVersionOneMigrationPreservesGrantsAndSecrets(t *testing.T) {
	dir := t.TempDir()
	vault, err := secure.Open(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "hqip.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	token := secure.Token()
	tokenEnc, _ := vault.Encrypt(token)
	passwordEnc, _ := vault.Encrypt("existing-proxy-password")
	agent := secure.Token()
	agentEnc, _ := vault.Encrypt(agent)
	n := model.Node{ID: "existing-node", Name: "existing", Domain: "old.example.com", SSHHost: "old.example.com", Port: 443, Status: "ready"}
	if _, err = db.Exec("INSERT INTO nodes(id,config,agent_hash,agent_secret) VALUES(?,?,?,?)", n.ID, jsonString(n), secure.Hash(agent), agentEnc); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO devices(id,name,created_at,token_hash,token_secret) VALUES('existing-device','old',?,?,?)", time.Now().Unix(), secure.Hash(token), tokenEnc); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO credentials(node_id,device_id,password) VALUES(?,'existing-device',?)", n.ID, passwordEnc); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(dir, "https://public.example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.Assignment("existing-device")
	if err != nil || len(a.NodeIDs) != 1 || a.DefaultNodeID != n.ID {
		t.Fatalf("migration lost grants: %+v %v", a, err)
	}
	d, err := s.SubscriptionDevice(token)
	if err != nil || d.ID != "existing-device" {
		t.Fatal("migration changed public token")
	}
	password, _ := s.Credential(n.ID, d.ID)
	if password != "existing-proxy-password" {
		t.Fatal("migration changed proxy credential")
	}
	newNode(t, s, "new-node")
	a, _ = s.Assignment(d.ID)
	if len(a.NodeIDs) != 1 {
		t.Fatal("new server was implicitly assigned to existing device")
	}
	newD, err := s.CreateDevice("new-device")
	if err != nil {
		t.Fatal(err)
	}
	a, _ = s.Assignment(newD.ID)
	if len(a.NodeIDs) != 0 || a.DefaultNodeID != "" {
		t.Fatal("new device inherited all node permissions")
	}
}

func TestClientTokensRotateExpireAndStayDeviceScoped(t *testing.T) {
	s, now := fixture(t)
	d, _ := s.CreateDevice("computer")
	if err := s.SetClientAccount(d.ID, "computer-user", "a-strong-device-password"); err != nil {
		t.Fatal(err)
	}
	tokens, err := s.ClientLogin("COMPUTER-USER", "a-strong-device-password", "install-001", "computer")
	if err != nil {
		t.Fatal(err)
	}
	id, session, err := s.ClientIdentity(tokens.AccessToken)
	if err != nil || id != d.ID || session != tokens.SessionID {
		t.Fatal("client access did not resolve its device")
	}
	var storedAccess, storedRefresh string
	if err = s.DB.QueryRow("SELECT access_hash,refresh_hash FROM client_sessions WHERE id=?", session).Scan(&storedAccess, &storedRefresh); err != nil {
		t.Fatal(err)
	}
	if storedAccess == tokens.AccessToken || storedRefresh == tokens.RefreshToken {
		t.Fatal("stored plaintext session token")
	}
	*now = now.Add(16 * time.Minute)
	if _, _, err = s.ClientIdentity(tokens.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired access token accepted")
	}
	refreshed, err := s.ClientRefresh(tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken == tokens.AccessToken || refreshed.RefreshToken == tokens.RefreshToken {
		t.Fatal("refresh did not rotate both secrets")
	}
	if _, err = s.ClientRefresh(tokens.RefreshToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("spent refresh token accepted")
	}
	if _, _, err = s.ClientIdentity(refreshed.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("refresh replay did not revoke session family")
	}
	tokens, err = s.ClientLogin("computer-user", "a-strong-device-password", "install-001", "computer")
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err = s.UpdateDevice(d.ID, nil, &disabled, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClientIdentity(tokens.AccessToken); err == nil {
		t.Fatal("disabled device session remained valid")
	}
}

func TestAssignmentRemovalWaitsForAgentAndEveryParticipatingClient(t *testing.T) {
	s, now := fixture(t)
	a := newNode(t, s, "a")
	b := newNode(t, s, "b")
	d, _ := s.CreateDevice("computer")
	initial, _ := s.Assignment(d.ID)
	assigned, err := s.Assign(d.ID, []string{a.ID, b.ID}, a.ID, initial.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetClientAccount(d.ID, "computer-user", "a-strong-device-password"); err != nil {
		t.Fatal(err)
	}
	one, _ := s.ClientLogin("computer-user", "a-strong-device-password", "install-001", "first")
	two, _ := s.ClientLogin("computer-user", "a-strong-device-password", "install-002", "second")
	beforeA, _ := s.Credential(a.ID, d.ID)
	beforeNode, _ := s.Node(a.ID)
	assigned, err = s.Assign(d.ID, []string{a.ID, b.ID}, b.ID, assigned.Revision)
	if err != nil {
		t.Fatal(err)
	}
	afterNode, _ := s.Node(a.ID)
	if beforeNode.DesiredRevision != afterNode.DesiredRevision {
		t.Fatal("default-only change revoked a still-assigned member")
	}
	if _, err = s.Assign(d.ID, []string{b.ID}, b.ID, assigned.Revision-1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale assignment revision accepted")
	}
	assigned, err = s.Assign(d.ID, []string{b.ID}, b.ID, assigned.Revision)
	if err != nil {
		t.Fatal(err)
	}
	removals, _ := s.Removals(d.ID)
	if len(removals) != 1 || removals[0].Deadline != nil || removals[0].Status != "pending" {
		t.Fatalf("offline node was incorrectly assigned a wall-clock deadline: %+v", removals)
	}
	desired, _ := s.Desired(a.ID)
	var command model.AgentDevice
	for _, entry := range desired.Devices {
		if entry.ID == d.ID {
			command = entry
		}
	}
	if !command.Enabled || command.RemovalID != removals[0].ID || command.RevokeAfterSeconds != 60 {
		t.Fatalf("missing grace command: %+v", command)
	}
	status := model.ClientStatus{AppliedConfigRevision: assigned.Revision, ProcessedConfigRevision: assigned.Revision, ActualNodeID: b.ID, DesiredNodeID: b.ID, RunState: "connected", Mode: "rule", ConnectionIntent: true}
	if err = s.ClientStatus(d.ID, one.SessionID, status); err != nil {
		t.Fatal(err)
	}
	removals, _ = s.Removals(d.ID)
	if removals[0].Immediate {
		t.Fatal("one client prematurely revoked the other client's grace")
	}
	status.RunState = "failed"
	status.ActualNodeID = ""
	status.AppliedConfigRevision = assigned.Revision - 1
	if err = s.ClientStatus(d.ID, two.SessionID, status); err != nil {
		t.Fatal(err)
	}
	removals, _ = s.Removals(d.ID)
	if !removals[0].Immediate {
		t.Fatal("all clients processed new profile but old authorization not revoked")
	}
	deadline := now.Add(60 * time.Second)
	err = s.Report(a.ID, model.AgentReport{Revision: desired.Revision, Removals: []model.AgentRemovalStatus{{ID: command.RemovalID, DeviceID: d.ID, Status: "scheduled", Deadline: deadline}}})
	if err != nil {
		t.Fatal(err)
	}
	removals, _ = s.Removals(d.ID)
	if removals[0].Deadline == nil || !removals[0].Deadline.Equal(deadline) {
		t.Fatal("Agent's persisted deadline was not recorded")
	}
	assigned, err = s.Assign(d.ID, []string{a.ID, b.ID}, a.ID, assigned.Revision)
	if err != nil {
		t.Fatal(err)
	}
	afterA, _ := s.Credential(a.ID, d.ID)
	if beforeA == afterA {
		t.Fatal("reassignment resurrected old revoked credentials")
	}
	desired, _ = s.Desired(a.ID)
	for _, entry := range desired.Devices {
		if entry.ID == d.ID && (!entry.Enabled || entry.RemovalID != "") {
			t.Fatal("old removal leaked into new grant")
		}
	}
}

func TestRulesCanonicalBytesAndRestoreInvalidateClientState(t *testing.T) {
	s, _ := fixture(t)
	d, _ := s.CreateDevice("computer")
	before, _ := s.Assignment(d.ID)
	version, err := s.SaveRules(rules.Default())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.RuleBytes(version.Hash)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := rules.Canonical(version.Bundle)
	if string(raw) != string(canonical) {
		t.Fatal("served rules differ from hash input")
	}
	after, _ := s.Assignment(d.ID)
	if after.Revision <= before.Revision {
		t.Fatal("rules update did not invalidate profile")
	}
	same, _ := s.SaveRules(rules.Default())
	if same.Version != version.Version {
		t.Fatal("same rule body advanced revision")
	}
	if err = s.SetClientAccount(d.ID, "computer-user", "a-strong-device-password"); err != nil {
		t.Fatal(err)
	}
	tokens, _ := s.ClientLogin("computer-user", "a-strong-device-password", "install-001", "computer")
	if err = s.ReconcileRestore(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClientIdentity(tokens.AccessToken); err == nil {
		t.Fatal("restored snapshot resumed old client session")
	}
	data, err := os.ReadFile(filepath.Join(s.DataDir, "hqip.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tokens.AccessToken) {
		t.Fatal("raw access token persisted to database")
	}
	var decoded rules.Bundle
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
}

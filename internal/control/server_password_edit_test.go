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

type passwordEditInstaller struct {
	managedInstaller
	passwords []string
	checkErr  error
}

func (i *passwordEditInstaller) CheckServer(ctx context.Context, server model.Server, password string, pin provision.PinFunc) (provision.ServerInspection, error) {
	i.passwords = append(i.passwords, password)
	if i.checkErr != nil {
		return provision.ServerInspection{}, i.checkErr
	}
	return i.managedInstaller.CheckServer(ctx, server, password, pin)
}

func TestServerEditBlankPasswordPreservesCredential(t *testing.T) {
	s, h := newServer(t)
	installer := &passwordEditInstaller{}
	s.runner = installer
	cookie, csrf := loginAdmin(t, s, h)
	const original = "saved-password-for-edit"
	form := map[string]any{"name": "original", "sshHost": "8.8.8.8", "password": original, "saveCredential": true, "expiresAt": s.Store.Now().Add(24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)}
	w := request(h, "POST", "/api/v1/servers", form, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var server model.Server
	if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/servers/" + server.ID
	for _, omitPassword := range []bool{true, false} {
		body := map[string]any{"name": "edited", "sshHost": "1.1.1.1", "sshPort": 30022, "sshUser": "admin", "expectedRevision": server.Revision, "saveCredential": false}
		if !omitPassword {
			body["password"] = ""
			body["sshPort"] = 30023
		}
		w = request(h, "PATCH", path, body, cookie, csrf)
		if w.Code != 200 {
			t.Fatalf("blank password edit: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), original) {
			t.Fatal("returned saved password")
		}
		if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
			t.Fatal(err)
		}
		if installer.passwords[len(installer.passwords)-1] != original {
			t.Fatal("SSH did not use original password")
		}
		if password, err := s.Store.ServerPassword(server); err != nil || password != original {
			t.Fatalf("saved password no longer usable: %v", err)
		}
		status, err := s.Store.ServerCredential(server.ID)
		if err != nil || status.Status != "healthy" {
			t.Fatalf("credential not rebound: %+v %v", status, err)
		}
	}
	body := map[string]any{"name": server.Name, "sshHost": server.SSHHost, "sshPort": server.SSHPort, "sshUser": server.SSHUser, "expectedRevision": server.Revision}
	// Failed validation must leave both connection and saved secret untouched.
	installer.checkErr = errors.New("authentication failed with " + original)
	body["sshPort"] = 30024
	w = request(h, "PATCH", path, body, cookie, csrf)
	if w.Code != 400 || strings.Contains(w.Body.String(), original) {
		t.Fatalf("failure leaked password: status %d", w.Code)
	}
	current, err := s.Store.Server(server.ID)
	if err != nil || current.Revision != server.Revision || current.SSHPort != server.SSHPort {
		t.Fatal("failed edit changed connection", err)
	}
	if password, err := s.Store.ServerPassword(current); err != nil || password != original {
		t.Fatal("failed edit replaced credential", err)
	}
	installer.checkErr = nil
	body["sshPort"] = server.SSHPort
	// Explicitly entered passwords retain the existing save/no-save behavior.
	for _, save := range []bool{false, true} {
		body["password"], body["saveCredential"], body["expectedRevision"] = "replacement-password", save, server.Revision
		w = request(h, "PATCH", path, body, cookie, csrf)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
			t.Fatal(err)
		}
		want := original
		if save {
			want = "replacement-password"
		}
		if password, err := s.Store.ServerPassword(server); err != nil || password != want {
			t.Fatal("explicit password persistence changed", err)
		}
	}
	checks := len(installer.passwords)
	body["password"], body["expectedRevision"] = "", server.Revision-1
	if w = request(h, "PATCH", path, body, cookie, csrf); w.Code != 409 || len(installer.passwords) != checks {
		t.Fatal("stale edit attempted SSH")
	}
}

func TestServerEditWithoutSavedPasswordRequiresExplicitPassword(t *testing.T) {
	s, h := newServer(t)
	installer := &passwordEditInstaller{}
	s.runner = installer
	cookie, csrf := loginAdmin(t, s, h)
	form := map[string]any{"name": "unsaved", "sshHost": "8.8.8.8", "password": "", "expiresAt": s.Store.Now().Add(24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)}
	if w := request(h, "POST", "/api/v1/servers", form, cookie, csrf); w.Code != 400 || len(installer.passwords) != 0 {
		t.Fatal("creation allowed empty password")
	}
	form["password"] = "one-time-password"
	w := request(h, "POST", "/api/v1/servers", form, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var server model.Server
	if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	delete(form, "expiresAt")
	form["password"], form["expectedRevision"] = "", server.Revision
	path := "/api/v1/servers/" + server.ID
	w = request(h, "PATCH", path, form, cookie, csrf)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "请填写密码后重试") || len(installer.passwords) != 1 {
		t.Fatal("missing saved password not explained", w.Body.String())
	}
	form["password"], form["saveCredential"] = "newly-saved-password", true
	w = request(h, "PATCH", path, form, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	if password, err := s.Store.ServerPassword(server); err != nil || password != "newly-saved-password" {
		t.Fatal("could not recover with explicit password", err)
	}
}

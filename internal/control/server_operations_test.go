package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/provision"
	"github.com/KeepS13/HQIP/internal/store"
)

type operationsInstaller struct {
	managedInstaller
	checkErr error
	upgraded int
}

func (i *operationsInstaller) CheckServer(ctx context.Context, v model.Server, p string, pin provision.PinFunc) (provision.ServerInspection, error) {
	if i.checkErr != nil {
		return provision.ServerInspection{}, i.checkErr
	}
	return i.managedInstaller.CheckServer(ctx, v, p, pin)
}
func (i *operationsInstaller) AgentRelease() (provision.AgentRelease, error) {
	return provision.AgentRelease{SourceSHA: strings.Repeat("a", 40)}, nil
}
func (i *operationsInstaller) UpgradeAgent(context.Context, provision.Input, string, string, provision.LogFunc, provision.PinFunc) error {
	i.upgraded++
	return nil
}

func TestManagedCredentialAPIAndPasswordRecovery(t *testing.T) {
	s, h := newServer(t)
	i := &operationsInstaller{}
	s.runner = i
	cookie, csrf := loginAdmin(t, s, h)
	form := map[string]any{"name": "managed", "sshHost": "203.0.113.10", "sshPort": 30022, "sshUser": "root", "password": "initial-secret-password", "saveCredential": true, "expiresAt": s.Store.Now().Add(24 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)}
	w := request(h, "POST", "/api/v1/servers", form, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var v model.Server
	json.Unmarshal(w.Body.Bytes(), &v)
	path := "/api/v1/servers/" + v.ID
	if w = request(h, "GET", path+"/credential", nil, cookie, csrf); w.Code != 200 || strings.Contains(w.Body.String(), "initial-secret") {
		t.Fatal(w.Body.String())
	}
	if w = request(h, "POST", path+"/password", map[string]any{"newPassword": "new-secret-password", "expectedRevision": v.Revision}, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	w = request(h, "POST", path+"/password", map[string]any{"newPassword": "new-secret-password", "expectedRevision": v.Revision}, cookie, csrf)
	if w.Code != http.StatusGone {
		t.Fatal("remote password mutation endpoint is still enabled", w.Code)
	}
	if w = request(h, "POST", "/api/v1/server-operations/legacy/retry", map[string]any{}, cookie, csrf); w.Code != http.StatusGone {
		t.Fatal("legacy password retry enabled")
	}
	// The provider already changed this password. Updating HQIP only checks it.
	i.checkErr = provision.ErrSSHAuthentication
	update := map[string]any{"password": "provider-new-password", "expectedRevision": v.Revision, "credentialRevision": 1}
	if w = request(h, "PUT", path+"/credential", update, cookie, csrf); w.Code != 400 {
		t.Fatal("invalid password saved")
	}
	if password, err := s.Store.ServerPassword(v); err != nil || password != "initial-secret-password" {
		t.Fatal("failed verification overwrote saved credential")
	}
	i.checkErr = nil
	if w = request(h, "PUT", path+"/credential", update, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if password, err := s.Store.ServerPassword(v); err != nil || password != "provider-new-password" {
		t.Fatal("verified credential not updated")
	}
	if jobs, err := s.Store.ServerOperations(v.ID); err != nil || len(jobs) != 0 {
		t.Fatal("credential update created a remote mutation job")
	}
	var j store.ServerOperation
	var err error
	for _, failure := range []error{provision.ErrSSHAuthentication, errors.New("network unreachable")} {
		i.checkErr = failure
		if _, err = s.Store.QueueServerOperation(v.ID, "", "check", "", "", 0); err != nil {
			t.Fatal(err)
		}
		j, err = s.Store.NextServerOperation()
		if err != nil {
			t.Fatal(err)
		}
		s.runServerOperation(context.Background(), j)
		status, _ := s.Store.ServerCredential(v.ID)
		want := "unreachable"
		if errors.Is(failure, provision.ErrSSHAuthentication) {
			want = "invalid"
		}
		if status.Status != want {
			t.Fatalf("status %s != %s", status.Status, want)
		}
	}
}
func TestAuthenticationDatabaseFailureReturnsUnavailable(t *testing.T) {
	s, _ := newServer(t)
	h := s.PublicHandler()
	if err := s.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v3/client/profile", "/api/v1/user/session", "/api/v3/client/refresh"} {
		method := "GET"
		body := ""
		if strings.HasSuffix(path, "/refresh") {
			method = "POST"
			body = `{"refreshToken":"` + strings.Repeat("x", 43) + `"}`
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 43))
		r.Header.Set("Cookie", "hqip_user="+strings.Repeat("x", 43))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if _, err := s.Store.PortalRefresh(strings.Repeat("x", 43)); errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("database failure revoked login")
	}
}

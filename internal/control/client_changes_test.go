package control

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func changeStreamLogin(t *testing.T, s *Server) (model.PortalUser, model.PortalTokens) {
	t.Helper()
	user, _, _ := portalLoginFixture(t, s, "changes@example.com")
	login, _, err := s.Store.PortalLogin(user.Email, "safe-user-password-123")
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("c", 43)
	hash := sha256.Sum256([]byte(verifier))
	redirect := "http://127.0.0.1:43210/callback"
	id, _, err := s.Store.BeginPortalAuthorization(base64.RawURLEncoding.EncodeToString(hash[:]), redirect, strings.Repeat("s", 32), "changes test", "changes-installation")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.Store.ApprovePortalAuthorization(id, user.ID, login.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(approved)
	tokens, err := s.Store.ExchangePortalCode(target.Query().Get("code"), verifier, redirect, "changes-installation")
	if err != nil {
		t.Fatal(err)
	}
	return user, tokens
}

// Real HTTP streaming verifies flushing beyond the ordinary write timeout,
// authentication, one stream/session, website changes and session revocation.
func TestNativeChangesStreamsBusinessUpdatesAndRevocation(t *testing.T) {
	s, _ := newServer(t)
	user, tokens := changeStreamLogin(t, s)
	public := s.PublicHandler()
	if w := bearerRequest(public, "GET", "/api/v3/client/changes", nil, ""); w.Code != 401 {
		t.Fatal("unauthenticated stream accepted", w.Code)
	}
	profile := bearerRequest(public, "GET", "/api/v3/client/profile", nil, tokens.AccessToken)
	if profile.Code != 200 {
		t.Fatal("initial profile failed", profile.Code)
	}
	ts := httptest.NewUnstartedServer(public)
	ts.Config.WriteTimeout = 500 * time.Millisecond
	ts.Start()
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v3/client/changes", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	response, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatal("stream rejected", response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	var first, second struct {
		ChangeToken string `json:"changeToken"`
		Reauth      bool   `json:"reauth"`
	}
	if err = decoder.Decode(&first); err != nil || len(first.ChangeToken) != 64 {
		t.Fatal("missing initial change marker", err)
	}
	if w := bearerRequest(public, "GET", "/api/v3/client/changes", nil, tokens.AccessToken); w.Code != 429 {
		t.Fatal("duplicate stream accepted", w.Code)
	}
	if _, err = s.Store.DB.Exec("UPDATE portal_users SET revision=revision+1 WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	if err = decoder.Decode(&second); err != nil || second.ChangeToken == first.ChangeToken || second.Reauth {
		t.Fatal("business update not streamed", err)
	}
	if err = s.Store.RevokePortalSession(user.ID, tokens.SessionID); err != nil {
		t.Fatal(err)
	}
	if err = decoder.Decode(&second); err != nil || !second.Reauth {
		t.Fatal("revoked session kept receiving catalog", err)
	}
	response.Body.Close()
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.clientChangesMu.Lock()
		count := len(s.clientChangesSessions)
		s.clientChangesMu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("closed stream retained its session slot")
}

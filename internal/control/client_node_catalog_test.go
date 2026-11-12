package control

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func catalogTestNode(t *testing.T, s *Server, name, ip string) model.Node {
	t.Helper()
	node, err := s.Store.CreateNode(model.Node{Name: name, SSHHost: name + ".example.com", SSHPort: 22, SSHUser: "root", PublicIP: ip, Domain: name + ".example.com", Port: 443}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestNativeProfileCatalogBeforeSelectionIsCredentialFreeAndUserScoped(t *testing.T) {
	s, _ := newServer(t)
	user, cookie, csrf := portalLoginFixture(t, s, "catalog-owner@example.com")
	other, _, _ := portalLoginFixture(t, s, "catalog-other@example.com")
	own := subscriptionFixture(t, s, user, nil, "", 2)
	_ = subscriptionFixture(t, s, user, own.NodeIDs, own.DefaultNodeID, 2)
	foreignNode := catalogTestNode(t, s, "foreign-catalog", "203.0.113.18")
	expiredNode := catalogTestNode(t, s, "expired-catalog", "203.0.113.19")
	foreign := subscriptionFixture(t, s, other, []string{foreignNode.ID}, foreignNode.ID, 1)
	expired := subscriptionFixture(t, s, user, []string{expiredNode.ID}, expiredNode.ID, 1)
	if _, err := s.Store.DB.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", s.Store.Now().Unix()-1, expired.ID); err != nil {
		t.Fatal(err)
	}
	public := s.PublicHandler()
	verifier := strings.Repeat("n", 43)
	digest := sha256.Sum256([]byte(verifier))
	redirect := "http://127.0.0.1:45231/callback"
	w := bearerRequest(public, "POST", "/api/v3/client/authorize", map[string]string{"codeChallenge": base64.RawURLEncoding.EncodeToString(digest[:]), "redirectUri": redirect, "state": strings.Repeat("s", 32), "clientName": "catalog test", "installationId": "catalog-test"}, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var begin struct {
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &begin)
	w = portalRequest(public, "POST", "/api/v1/user/authorize/"+begin.RequestID, map[string]any{}, cookie, csrf, s.opts.PublicURL)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var approved struct {
		RedirectURL string `json:"redirectUrl"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &approved)
	target, _ := url.Parse(approved.RedirectURL)
	w = bearerRequest(public, "POST", "/api/v3/client/token", map[string]string{"code": target.Query().Get("code"), "codeVerifier": verifier, "redirectUri": redirect, "installationId": "catalog-test"}, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var tokens model.PortalTokens
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	w = bearerRequest(public, "GET", "/api/v3/client/profile", nil, tokens.AccessToken)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var profile struct {
		SubscriptionID string             `json:"subscriptionId"`
		Nodes          []model.ClientNode `json:"nodes"`
		Available      []map[string]any   `json:"availableNodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.SubscriptionID != "" || len(profile.Nodes) != 0 || len(profile.Available) != 1 || profile.Available[0]["id"] != own.NodeIDs[0] {
		t.Fatal("catalog changed selection or included inaccessible nodes", w.Body.String())
	}
	for _, key := range []string{"password", "sshPassword", "sshHost"} {
		if _, ok := profile.Available[0][key]; ok {
			t.Fatal("catalog exposed a secret field", key)
		}
	}
	if strings.Contains(w.Body.String(), foreign.NodeIDs[0]) {
		t.Fatal("another user's node leaked in profile")
	}
	var credentials int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM portal_credentials WHERE user_id=?", user.ID).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatal("listing generated proxy credentials", credentials, err)
	}
}

package control

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
)

func TestNativeProtocolOptInAndCredentialFreeCatalog(t *testing.T) {
	s, _ := newServer(t)
	user, cookie, csrf := portalLoginFixture(t, s, "reality-native@example.com")
	sub := subscriptionFixture(t, s, user, nil, "", 2)
	allocated, err := s.Store.Node(sub.NodeIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	endpoint := model.RealityEndpoint{Port: 8443, ServerName: model.DefaultRealityServerName, PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortID: "0123456789abcdef"}
	raw, _ := json.Marshal(endpoint)
	if _, err := s.Store.DB.Exec("INSERT INTO metadata(key,value) VALUES(?,?)", "vless-reality:"+sub.NodeIDs[0], string(raw)); err != nil {
		t.Fatal(err)
	}
	h := s.PublicHandler()
	w := portalRequest(h, "GET", "/api/v1/user/subscriptions/"+sub.ID, nil, cookie, csrf, s.opts.PublicURL)
	var detail struct{ Nodes []model.ClientNode }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || len(detail.Nodes) != 2 {
		t.Fatal("missing user-facing endpoints", w.Code, w.Body.String())
	}
	for _, node := range detail.Nodes {
		if node.Server != allocated.Domain || node.PublicIP != allocated.PublicIP {
			t.Fatal("user node address is not its allocated domain")
		}
	}
	verifier := strings.Repeat("r", 43)
	digest := sha256.Sum256([]byte(verifier))
	redirect := "http://127.0.0.1:45231/callback"
	w = bearerRequest(h, "POST", "/api/v3/client/authorize", map[string]string{"codeChallenge": base64.RawURLEncoding.EncodeToString(digest[:]), "redirectUri": redirect, "state": strings.Repeat("s", 32), "clientName": "Reality test", "installationId": "reality-native"}, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var begin struct {
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &begin)
	w = portalRequest(h, "POST", "/api/v1/user/authorize/"+begin.RequestID, map[string]any{}, cookie, csrf, s.opts.PublicURL)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var approval struct {
		RedirectURL string `json:"redirectUrl"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &approval)
	target, _ := url.Parse(approval.RedirectURL)
	w = bearerRequest(h, "POST", "/api/v3/client/token", map[string]string{"code": target.Query().Get("code"), "codeVerifier": verifier, "redirectUri": redirect, "installationId": "reality-native"}, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var tokens model.PortalTokens
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	for _, test := range []struct {
		query string
		count int
	}{{"", 1}, {"?protocols=" + model.RealityCapability, 2}} {
		w = bearerRequest(h, "GET", "/api/v3/client/profile"+test.query, nil, tokens.AccessToken)
		var profile struct {
			Nodes     []model.ClientNode
			Available []map[string]any `json:"availableNodes"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &profile) != nil || len(profile.Available) != test.count || len(profile.Nodes) != 0 {
			t.Fatal("protocol opt-in/catalog mismatch", w.Code, w.Body.String())
		}
		for _, node := range profile.Available {
			if node["server"] != allocated.Domain {
				t.Fatal("catalog must advertise the allocated domain")
			}
			if _, found := node["password"]; found {
				t.Fatal("catalog exposes credentials")
			}
		}
	}
	if _, err := s.Store.SelectSubscription(user.ID, tokens.SessionID, sub.ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		count int
	}{{"", 1}, {"?protocols=" + model.RealityCapability, 2}} {
		w = bearerRequest(h, "GET", "/api/v3/client/profile"+test.query, nil, tokens.AccessToken)
		var profile model.ClientProfile
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &profile) != nil || len(profile.Nodes) != test.count {
			t.Fatal("selected profile opt-in mismatch", w.Code, w.Body.String())
		}
		if test.count == 2 && profile.Nodes[1].Password != model.RealityUUID(profile.Nodes[0].Password) {
			t.Fatal("native protocols have inconsistent credentials")
		}
		for _, node := range profile.Nodes {
			if node.Server != allocated.Domain || (node.Protocol == "vless" && node.SNI != endpoint.ServerName) {
				t.Fatal("native dial address and Reality SNI were conflated")
			}
		}
	}
}

func TestDualProtocolSubscriptionExports(t *testing.T) {
	s, user, sub, node, link := portalExportFixture(t)
	endpoint := model.RealityEndpoint{Port: 8443, ServerName: model.DefaultRealityServerName, PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortID: "0123456789abcdef"}
	raw, _ := json.Marshal(endpoint)
	if _, err := s.Store.DB.Exec("INSERT INTO metadata(key,value) VALUES(?,?)", "vless-reality:"+node.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	h := s.PublicHandler()
	var clash struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	w := request(h, "GET", link, nil, "", "")
	if w.Code != 200 || yaml.Unmarshal(w.Body.Bytes(), &clash) != nil || len(clash.Proxies) != 2 {
		t.Fatal("dual Clash export failed", w.Code)
	}
	password, _ := clash.Proxies[0]["password"].(string)
	if clash.Proxies[1]["uuid"] != model.RealityUUID(password) || clash.Proxies[1]["type"] != "vless" || clash.Proxies[1]["reality-opts"] == nil {
		t.Fatal("Clash Reality auth mismatch")
	}
	if clash.Proxies[0]["server"] != node.Domain || clash.Proxies[1]["server"] != node.Domain ||
		clash.Proxies[0]["sni"] != node.Domain || clash.Proxies[1]["servername"] != endpoint.ServerName {
		t.Fatal("Clash address must be the node domain with protocol-specific SNI")
	}
	w = request(h, "GET", link+"?format=v2ray", nil, "", "")
	decoded, err := base64.StdEncoding.DecodeString(w.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	links := strings.Split(string(decoded), "\n")
	if len(links) != 2 {
		t.Fatal("missing VLESS URI")
	}
	vless, err := url.Parse(links[1])
	if err != nil {
		t.Fatal(err)
	}
	if vless.Scheme != "vless" || vless.Hostname() != node.Domain || vless.User.Username() != model.RealityUUID(password) || vless.Query().Get("pbk") != endpoint.PublicKey || vless.Query().Get("security") != "reality" || vless.Query().Get("sni") != endpoint.ServerName {
		t.Fatal("invalid VLESS URI")
	}
	w = request(h, "GET", link+"?format=singbox", nil, "", "")
	var sing struct{ Outbounds []map[string]any }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sing) != nil {
		t.Fatal("sing-box export failed")
	}
	counts := map[string]int{}
	for _, out := range sing.Outbounds {
		kind, _ := out["type"].(string)
		counts[kind]++
		if (kind == "vless" || kind == "hysteria2") && out["server"] != node.Domain {
			t.Fatal("sing-box address must be the allocated domain")
		}
		if kind == "vless" && out["uuid"] != model.RealityUUID(password) {
			t.Fatal("sing-box UUID mismatch")
		}
	}
	if counts["hysteria2"] != 1 || counts["vless"] != 1 {
		t.Fatal("export missing a transport", counts)
	}
	if _, err = s.Store.PortalSubscription(user.ID, sub.ID, "revoke"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?format=v2ray", "?format=singbox"} {
		if got := request(h, "GET", link+suffix, nil, "", ""); got.Code != 404 {
			t.Fatal("revoked link exposed protocol credentials", got.Code)
		}
	}
}

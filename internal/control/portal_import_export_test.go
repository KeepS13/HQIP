package control

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPortalCombinedExportPreservesIndependentCredentials(t *testing.T) {
	s, user, first, node, _ := portalExportFixture(t)
	second := subscriptionFixture(t, s, user, []string{node.ID}, node.ID, 2)
	link, err := s.Store.PortalAllSubscriptions(user.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	passwords := map[string]bool{}
	for _, id := range []string{first.ID, second.ID} {
		password, err := s.Store.PortalCredential(user.ID, id, node.ID, "subscription")
		if err != nil {
			t.Fatal(err)
		}
		passwords[password] = true
	}
	if len(passwords) != 2 {
		t.Fatal("subscriptions share credentials")
	}
	h := s.PublicHandler()
	for _, format := range []string{"clash", "v2ray", "singbox"} {
		response := request(h, "GET", link+"?format="+format+"&download=1", nil, "", "")
		if response.Code != 200 {
			t.Fatalf("%s: status %d", format, response.Code)
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Disposition") == "" {
			t.Fatal("missing export headers")
		}
		seen, names := map[string]bool{}, map[string]bool{}
		switch format {
		case "clash":
			var config struct {
				Proxies []struct{ Name, Password string }
			}
			if err = yaml.Unmarshal(response.Body.Bytes(), &config); err != nil {
				t.Fatal(err)
			}
			for _, p := range config.Proxies {
				seen[p.Password], names[p.Name] = true, true
			}
		case "v2ray":
			data, err := base64.StdEncoding.DecodeString(response.Body.String())
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(data), "\n") {
				uri, err := url.Parse(line)
				if err != nil {
					t.Fatal(err)
				}
				seen[uri.User.Username()], names[uri.Fragment] = true, true
			}
		case "singbox":
			var config struct {
				Outbounds []struct{ Tag, Password string }
			}
			if err = json.Unmarshal(response.Body.Bytes(), &config); err != nil {
				t.Fatal(err)
			}
			for _, p := range config.Outbounds {
				if p.Password != "" {
					seen[p.Password], names[p.Tag] = true, true
				}
			}
		}
		if len(seen) != 2 || len(names) != 2 {
			t.Fatal("duplicate server collapsed independent subscriptions", format)
		}
		for password := range seen {
			if !passwords[password] {
				t.Fatal("unexpected credential", format)
			}
		}
	}
	// Expiry removes only that member's nodes; it cannot consume another quota.
	if _, err = s.Store.DB.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", s.Store.Now().Unix()-1, second.ID); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", link+"?format=v2ray", nil, "", "")
	decoded, err := base64.StdEncoding.DecodeString(w.Body.String())
	if w.Code != 200 || err != nil || len(strings.Split(string(decoded), "\n")) != 1 {
		t.Fatal("expired member was not omitted", w.Code, err)
	}
	if _, err = s.Store.PortalAllSubscriptions(user.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	if w = request(h, "GET", link, nil, "", ""); w.Code != 404 {
		t.Fatal("old combined URL accepted", w.Code)
	}
}

func TestPortalCombinedImportAPIRequiresBrowserAndCSRF(t *testing.T) {
	s, _ := newServer(t)
	user, cookie, csrf := portalLoginFixture(t, s, "combined-api@example.com")
	first := subscriptionFixture(t, s, user, nil, "", 2)
	second := subscriptionFixture(t, s, user, first.NodeIDs, first.NodeIDs[0], 2)
	h := s.PublicHandler()
	path := "/api/v1/user/import-link"
	if w := portalRequest(h, "POST", path, map[string]any{}, "", "", s.opts.PublicURL); w.Code != 401 {
		t.Fatal("anonymous mutation", w.Code)
	}
	if w := portalRequest(h, "POST", path, map[string]any{}, cookie, "", s.opts.PublicURL); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	for _, id := range []string{first.ID, second.ID} {
		if link, err := s.Store.PortalSubscription(user.ID, id, "get"); err != nil || link != "" {
			t.Fatal("unauthorized mutation created link", err)
		}
	}
	w := portalRequest(h, "POST", path, map[string]any{}, cookie, csrf, s.opts.PublicURL)
	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.URL == "" {
		t.Fatal("prepare", w.Code, err)
	}
	for _, method := range []string{"GET", "POST"} {
		again := portalRequest(h, method, path, map[string]any{}, cookie, csrf, s.opts.PublicURL)
		if again.Code != 200 || again.Body.String() != w.Body.String() {
			t.Fatal("unstable combined link", again.Code)
		}
	}
	if response := request(h, "GET", "/api/v1/user/subscriptions/"+first.ID+"/import-link", nil, cookie, ""); response.Code != 200 {
		t.Fatal("single link GET", response.Code)
	}
}

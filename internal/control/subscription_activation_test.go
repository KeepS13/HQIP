package control

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
)

func TestPendingSingBoxPinnedCoreCheck(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX")
	if binary == "" {
		t.Skip("HQIP_SINGBOX is required for the pinned parser check")
	}
	version, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "sing-box version 1.14.4") {
		t.Fatal("expected pinned sing-box 1.14.4", err)
	}
	body, _, err := pendingPortalConfig("singbox", "https://hqip.sharevps.online", "https://cms-hqip.sharevps.online")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "pending.json")
	if err = os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "check", "-c", file).CombinedOutput(); err != nil {
		t.Fatalf("pending config rejected: %v\n%s", err, output)
	}
}

func TestPendingSubscriptionLinkAndAdminActivation(t *testing.T) {
	s, adminHandler := newServer(t)
	admin, csrf := loginAdmin(t, s, adminHandler)
	user, cookie, userCSRF := portalLoginFixture(t, s, "custom-http@example.com")
	node := allocationNode(t, s)
	price := int64(1000)
	template, err := s.Store.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "custom", Enabled: true, Listed: true, PriceCents: &price, MaxConnections: 2, ValidityDays: 30, NodesPerSubscription: 1, NodeIDs: []string{}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.Store.AssignSubscription(user.ID, template.ID, "custom-http-grant", -1, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	h := s.PublicHandler()
	path := fmt.Sprintf("/api/v1/user/subscriptions/%s/import-link", sub.ID)
	w := portalRequest(h, "POST", path, map[string]any{}, cookie, userCSRF, s.opts.PublicURL)
	var result struct {
		URL string `json:"url"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.URL == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	link := result.URL
	for _, format := range []string{"clash", "singbox", "v2ray"} {
		w = request(h, "GET", link+"?format="+format+"&download=1", nil, "", "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Disposition") == "" {
			t.Fatal(format, w.Code, w.Body.String())
		}
		switch format {
		case "clash":
			var c struct {
				Proxies []any    `yaml:"proxies"`
				Rules   []string `yaml:"rules"`
			}
			if err = yaml.Unmarshal(w.Body.Bytes(), &c); err != nil || len(c.Proxies) != 0 || len(c.Rules) < 2 || c.Rules[len(c.Rules)-1] != "MATCH,REJECT" {
				t.Fatal(c, err)
			}
		case "singbox":
			var c struct {
				Outbounds []struct{ Type string } `json:"outbounds"`
				Route     struct {
					Rules []struct {
						Action string `json:"action"`
					} `json:"rules"`
				} `json:"route"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &c); err != nil || len(c.Outbounds) != 1 || c.Outbounds[0].Type != "direct" || len(c.Route.Rules) < 2 || c.Route.Rules[len(c.Route.Rules)-1].Action != "reject" {
				t.Fatal(c, err)
			}
		case "v2ray":
			decoded, err := base64.StdEncoding.DecodeString(w.Body.String())
			if err != nil || len(decoded) != 0 {
				t.Fatal(string(decoded), err)
			}
		}
	}
	current, err := s.Store.UserSubscription(user.ID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	path = fmt.Sprintf("/api/v1/users/%s/subscriptions/%s/nodes", user.ID, sub.ID)
	body := map[string]any{"nodeIds": []string{node.ID}, "expectedRevision": current.Revision}
	if w = request(adminHandler, "PATCH", path, body, cookie, userCSRF); w.Code != 401 {
		t.Fatal("user activated subscription", w.Code)
	}
	if w = request(h, "PATCH", path, body, admin, csrf); w.Code != 404 {
		t.Fatal("public admin mutation exposed", w.Code)
	}
	activation := s.Store.Now().UTC().Truncate(time.Second)
	w = request(adminHandler, "PATCH", path, body, admin, csrf)
	var activated model.UserSubscription
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &activated) != nil || activated.ActivationPending || activated.Status != "active" || !activated.ExpiresAt.Equal(activation.Add(30*24*time.Hour)) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, format := range []string{"clash", "singbox", "v2ray"} {
		w = request(h, "GET", link+"?format="+format, nil, "", "")
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal("same link did not activate", format, w.Code, w.Body.String())
		}
	}
	if got, err := s.Store.PortalSubscription(user.ID, sub.ID, "get"); err != nil || got != link {
		t.Fatal("link changed", err)
	}
	if _, err = s.Store.PortalSubscription(user.ID, sub.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	if w = request(h, "GET", link, nil, "", ""); w.Code != 404 {
		t.Fatal("revoked link accepted", w.Code)
	}
	if strings.Contains(link, "password") {
		t.Fatal("unexpected secret in URL")
	}
}

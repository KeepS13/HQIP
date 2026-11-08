package control

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
)

func TestPortalV2RaySubscriptionURIEncoding(t *testing.T) {
	password := "user:复杂 +/%?@#secret"
	profile := model.ClientProfile{DefaultNodeID: "ipv6", Nodes: []model.ClientNode{
		{ID: "first", Name: "普通节点", Server: "203.0.113.8", Port: 443, Password: "safe", SNI: "first.example.com", Ready: true},
		{ID: "ipv6", Name: "香港 #2\n测试", Server: "2001:db8::8", Port: 8443, Password: password, SNI: "tls.example.com", Ready: true},
		{ID: "hidden", Name: "尚未就绪", Ready: false},
	}}
	body, err := portalV2RaySubscription(profile)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatal(err)
	}
	links := strings.Split(string(decoded), "\n")
	if len(links) != 2 {
		t.Fatal("expected exactly two ready nodes")
	}
	first, err := url.Parse(links[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.Scheme != "hysteria2" || first.Hostname() != "2001:db8::8" || first.Port() != "8443" || first.User.Username() != password || first.Fragment != "香港 #2\n测试 · ipv6" {
		t.Fatal("URI failed to preserve the default node, IPv6, authentication or name")
	}
	if _, separatePassword := first.User.Password(); separatePassword {
		t.Fatal("authentication token was split into user/password fields")
	}
	if first.Query().Get("sni") != "tls.example.com" || first.Query().Get("insecure") != "0" || len(first.Query()) != 2 {
		t.Fatal("TLS parameters changed")
	}
	if strings.Contains(string(decoded), "复杂") || strings.Contains(string(decoded), "尚未就绪") {
		t.Fatal("raw credentials or unavailable node leaked into URI list")
	}
	profile.Nodes[0].Ready = false
	profile.Nodes[1].Ready = false
	if _, err = portalV2RaySubscription(profile); err == nil {
		t.Fatal("empty node list accepted")
	}
}

func TestPortalExportFormatWhitelist(t *testing.T) {
	for _, tc := range []struct{ query, want string }{{"", "clash"}, {"format=clash", "clash"}, {"format=v2ray", "v2ray"}, {"format=", ""}, {"format=json", ""}, {"format=V2Ray", ""}, {"format=v2ray&format=clash", ""}, {"format=%zz", ""}} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := portalExportFormat(tc.query)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("format=%q, error=%v", got, err)
			}
		})
	}
}

func portalExportFixture(t *testing.T) (*Server, model.PortalUser, model.UserSubscription, model.Node, string) {
	t.Helper()
	s, _ := newServer(t)
	u, _, _ := portalLoginFixture(t, s, "formats@example.com")
	n, err := s.Store.CreateNode(model.Node{Name: "香港节点", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetStatus(n.ID, "ready", "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]' WHERE id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	sub := subscriptionFixture(t, s, u, []string{n.ID}, n.ID, 2)
	template, err := s.Store.SubscriptionTemplate(sub.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	quota := int64(50_000_000)
	template.QuotaBytes = &quota
	if _, err = s.Store.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	link, err := s.Store.PortalSubscription(u.ID, sub.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	return s, u, sub, n, link
}

func TestPortalSubscriptionFormatsUseSameScopedCredentials(t *testing.T) {
	s, _, sub, _, link := portalExportFixture(t)
	h := s.PublicHandler()
	for _, query := range []string{"?format=unsupported", "?format=v2ray&format=clash", "?format="} {
		if w := request(h, "GET", link+query, nil, "", ""); w.Code != 400 {
			t.Fatal("invalid format accepted", w.Code)
		}
	}
	var credentials int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM portal_credentials WHERE subscription_id=?", sub.ID).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatal("invalid-format request created credentials", err)
	}
	clash := request(h, "GET", link, nil, "", "")
	v2ray := request(h, "GET", link+"?format=v2ray", nil, "", "")
	if clash.Code != 200 || v2ray.Code != 200 {
		t.Fatal("format export failed", clash.Code, v2ray.Code)
	}
	if !strings.HasPrefix(clash.Header().Get("Content-Type"), "text/yaml") || !strings.HasPrefix(v2ray.Header().Get("Content-Type"), "text/plain") || v2ray.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect content type or secret caching")
	}
	var config struct {
		Proxies []struct {
			Password, Server, SNI string
			Port                  int
			SkipVerify            bool `yaml:"skip-cert-verify"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(clash.Body.Bytes(), &config); err != nil || len(config.Proxies) != 1 {
		t.Fatal("invalid Clash export", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(v2ray.Body.String())
	if err != nil {
		t.Fatal("invalid Base64 export", err)
	}
	uri, err := url.Parse(string(decoded))
	if err != nil || uri.User.Username() != config.Proxies[0].Password || uri.Hostname() != config.Proxies[0].Server || uri.Query().Get("sni") != config.Proxies[0].SNI || config.Proxies[0].SkipVerify {
		t.Fatal("export formats changed credentials, endpoint or TLS verification")
	}
	if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM portal_credentials WHERE subscription_id=? AND scope='subscription'", sub.ID).Scan(&credentials); err != nil || credentials != 1 {
		t.Fatal("formats created different credential scopes", err)
	}
}

func TestPortalSubscriptionFormatsRejectUnavailableAccess(t *testing.T) {
	for _, action := range []string{"expired", "exhausted", "disabled", "removed", "link-revoked", "node-unready"} {
		t.Run(action, func(t *testing.T) {
			s, user, sub, node, link := portalExportFixture(t)
			var err error
			want := 403
			switch action {
			case "expired":
				_, err = s.Store.DB.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", s.Store.Now().Add(-time.Hour).Unix(), sub.ID)
			case "exhausted":
				_, err = s.Store.DB.Exec("UPDATE portal_usage SET upload=50000000 WHERE subscription_id=?", sub.ID)
			case "disabled":
				err = s.Store.SetPortalEnabled(user.ID, false)
				want = 404
			case "removed":
				_, err = s.Store.DB.Exec("UPDATE portal_subscriptions SET revoked=1 WHERE id=?", sub.ID)
				want = 404
			case "link-revoked":
				_, err = s.Store.PortalSubscription(user.ID, sub.ID, "revoke")
				want = 404
			case "node-unready":
				err = s.Store.SetStatus(node.ID, "pending_verification", "test", false)
				want = 503
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"", "?format=clash", "?format=v2ray"} {
				w := request(s.PublicHandler(), "GET", link+suffix, nil, "", "")
				if w.Code != want {
					t.Fatal("inactive export response", suffix, w.Code, "want", want)
				}
				if strings.Contains(w.Body.String(), "hysteria2://") || strings.Contains(w.Body.String(), "password:") {
					t.Fatal("inactive subscription exposed credentials")
				}
			}
		})
	}
}

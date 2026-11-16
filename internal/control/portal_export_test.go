package control

import (
	"encoding/base64"
	"encoding/json"
	"mime"
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
	if first.Scheme != "hysteria2" || first.Hostname() != "tls.example.com" || first.Port() != "8443" || first.User.Username() != password || first.Fragment != "香港 #2\n测试" {
		t.Fatal("URI failed to preserve the default node, domain, authentication or name")
	}
	second, err := url.Parse(links[1])
	if err != nil || second.Hostname() != "first.example.com" || second.Query().Get("sni") != "first.example.com" {
		t.Fatal("URI address must use each node's domain instead of its public IP")
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
	for _, tc := range []struct{ query, want string }{{"", "clash"}, {"format=clash", "clash"}, {"format=v2ray", "v2ray"}, {"format=singbox", "singbox"}, {"format=singbox&download=1", "singbox"}, {"format=", ""}, {"format=json", ""}, {"format=V2Ray", ""}, {"format=v2ray&format=clash", ""}, {"format=singbox&format=singbox", ""}, {"format=%zz", ""}} {
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
	if _, err = s.Store.DB.Exec(`UPDATE nodes SET capabilities='["admission-v1"]',last_seen=?,core_healthy=1 WHERE id=?`, s.Store.Now().Unix(), n.ID); err != nil {
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
	s, _, sub, node, link := portalExportFixture(t)
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
	singbox := request(h, "GET", link+"?format=singbox", nil, "", "")
	if clash.Code != 200 || v2ray.Code != 200 || singbox.Code != 200 {
		t.Fatal("format export failed", clash.Code, v2ray.Code, singbox.Code)
	}
	if !strings.HasPrefix(clash.Header().Get("Content-Type"), "text/yaml") || !strings.HasPrefix(v2ray.Header().Get("Content-Type"), "text/plain") || !strings.HasPrefix(singbox.Header().Get("Content-Type"), "application/json") || v2ray.Header().Get("Cache-Control") != "no-store" || singbox.Header().Get("Cache-Control") != "no-store" {
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
	if err != nil || uri.User.Username() != config.Proxies[0].Password || uri.Hostname() != node.Domain || config.Proxies[0].Server != node.PublicIP || uri.Query().Get("sni") != config.Proxies[0].SNI || config.Proxies[0].SkipVerify {
		t.Fatal("export formats changed credentials, endpoint or TLS verification")
	}
	var singConfig portalSingBoxTestConfig
	if err = json.Unmarshal(singbox.Body.Bytes(), &singConfig); err != nil {
		t.Fatal("invalid Sing Box JSON", err)
	}
	if len(singConfig.Outbounds) != 3 || singConfig.Outbounds[1].Password != config.Proxies[0].Password || singConfig.Outbounds[1].Server != config.Proxies[0].Server || singConfig.Outbounds[1].ServerPort != config.Proxies[0].Port || singConfig.Outbounds[1].TLS.ServerName != config.Proxies[0].SNI || !singConfig.Outbounds[1].TLS.Enabled || singConfig.Outbounds[1].TLS.Insecure {
		t.Fatal("Sing Box changed credentials, endpoint or TLS verification")
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
			for _, suffix := range []string{"", "?format=clash", "?format=v2ray", "?format=singbox", "?format=singbox&download=1", "?format=clash&download=1", "?format=v2ray&download=1"} {
				w := request(s.PublicHandler(), "GET", link+suffix, nil, "", "")
				if w.Code != want {
					t.Fatal("inactive export response", suffix, w.Code, "want", want)
				}
				if strings.Contains(w.Body.String(), "hysteria2://") || strings.Contains(w.Body.String(), "password:") || strings.Contains(w.Body.String(), `"password"`) || w.Header().Get("Content-Disposition") != "" {
					t.Fatal("inactive subscription exposed credentials")
				}
			}
		})
	}
}

func TestPortalSubscriptionDownloadHeaders(t *testing.T) {
	s, _, _, _, link := portalExportFixture(t)
	for _, tc := range []struct{ format, extension string }{{"clash", "yaml"}, {"v2ray", "txt"}, {"singbox", "json"}} {
		t.Run(tc.format, func(t *testing.T) {
			plain := request(s.PublicHandler(), "GET", link+"?format="+tc.format, nil, "", "")
			download := request(s.PublicHandler(), "GET", link+"?format="+tc.format+"&download=1", nil, "", "")
			if plain.Code != 200 || download.Code != 200 || plain.Body.String() != download.Body.String() {
				t.Fatal("download changed the subscription contents or failed")
			}
			media, params, err := mime.ParseMediaType(download.Header().Get("Content-Disposition"))
			if err != nil || media != "attachment" || params["filename"] != "hqip-"+tc.format+"."+tc.extension {
				t.Fatal("incorrect attachment filename", err)
			}
			if plain.Header().Get("Content-Disposition") != "" || download.Header().Get("Content-Type") != plain.Header().Get("Content-Type") || download.Header().Get("Cache-Control") != "no-store" || download.Header().Get("Profile-Update-Interval") != "1" {
				t.Fatal("download changed subscription headers")
			}
		})
	}
}

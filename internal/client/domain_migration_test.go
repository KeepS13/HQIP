package client

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
)

// Retired addresses are only fixtures: they must never gain official trust.
const legacyServerURL = "https://api-hqip.5202345.xyz"
const legacyUserSiteURL = "https://hqip.5202345.xyz"

func TestOfficialAuthorizationOriginPair(t *testing.T) {
	api, _ := url.Parse(DefaultServerURL)
	for _, v := range []struct {
		url string
		ok  bool
	}{
		{"https://hqip.sharevps.online/authorize?request=abc", true},
		{"https://hqip.sharevps.online.evil.example/authorize", false},
		{"https://hqip.sharevps.online:444/authorize", false},
		{"http://hqip.sharevps.online/authorize", false},
		{"https://cms-hqip.sharevps.online/authorize", false},
		{"https://hqip.sharevps.online/login", false},
		{"https://hqip.sharevps.online/authorize/", false},
		{"https://hqip.sharevps.online/%61uthorize", false},
		{"https://hqip.sharevps.online/authorize#fragment", false},
		{"https://other@hqip.sharevps.online/authorize", false},
		{"https://hqip.sharevps.online@evil.example/authorize", false},
		{"https://hqip.5202345.xyz/authorize", false},
	} {
		page, _ := url.Parse(v.url)
		if trustedAuthorizationOrigin(api, page) != v.ok {
			t.Fatal(v.url)
		}
	}
	page, _ := url.Parse(UserSiteURL + "/authorize")
	for _, address := range []string{previousOfficialServerURL, "https://custom.example", "https://api-hqip.sharevps.online.evil.example", "https://api-hqip.sharevps.online:444", "http://api-hqip.sharevps.online", legacyServerURL, legacyUserSiteURL} {
		custom, _ := url.Parse(address)
		if trustedAuthorizationOrigin(custom, page) {
			t.Fatal("custom server gained official origin trust", address)
		}
	}
	legacy, _ := url.Parse(legacyServerURL)
	legacyPage, _ := url.Parse(legacyUserSiteURL + "/authorize")
	if trustedAuthorizationOrigin(legacy, legacyPage) {
		t.Fatal("retired API and portal retained cross-origin authorization trust")
	}
	for _, address := range []string{"https://custom.example", "https://custom.example:444", legacyServerURL, legacyUserSiteURL} {
		custom, _ := url.Parse(address)
		customPage, _ := url.Parse(address + "/authorize")
		if !trustedAuthorizationOrigin(custom, customPage) {
			t.Fatal("custom same-origin authorization changed", address)
		}
	}
}

func TestSavedOfficialURLMigrationLeavesCustomServerAndTokens(t *testing.T) {
	for _, address := range []string{legacyUserSiteURL, legacyUserSiteURL + "/", legacyServerURL, legacyServerURL + "/", DefaultServerURL, DefaultServerURL + "/", previousOfficialServerURL, previousOfficialServerURL + "/", "https://custom.example", "https://api-hqip.sharevps.online.evil.example", "https://api-hqip.sharevps.online:444", "https://api-hqip.5202345.xyz:444"} {
		t.Run(address, func(t *testing.T) {
			dir := t.TempDir()
			saved := persisted{ServerURL: address, AccountVersion: 3, Mode: "rule", InstallationID: "preserved-installation", SelectedSubscriptionID: "subscription-a", SelectedNodeID: "node-a", Preferences: map[string]SelectionPreference{"account-a": {SubscriptionID: "subscription-a", NodeIDs: map[string]string{"subscription-a": "node-a"}}}, Tokens: Tokens{RefreshToken: "stored-refresh", AccessToken: "stored-access", SessionID: "stored-session"}}
			b, _ := json.Marshal(saved)
			if err := os.WriteFile(filepath.Join(dir, "session.dat"), b, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := New(dir, fakePlatform{}, &fakeEngine{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			want := address
			if address == DefaultServerURL || address == DefaultServerURL+"/" || address == previousOfficialServerURL || address == previousOfficialServerURL+"/" {
				want = DefaultServerURL
			}
			if c.saved.ServerURL != want || c.view.ServerURL != want || c.api.base != strings.TrimRight(want, "/") || c.api.Tokens() != saved.Tokens || c.saved.InstallationID != saved.InstallationID || c.saved.SelectedSubscriptionID != saved.SelectedSubscriptionID || c.saved.SelectedNodeID != saved.SelectedNodeID || !reflect.DeepEqual(c.saved.Preferences, saved.Preferences) {
				t.Fatal("normalization changed identity or custom URL")
			}
			raw, err := os.ReadFile(filepath.Join(dir, "session.dat"))
			if err != nil {
				t.Fatal(err)
			}
			var stored persisted
			if err = json.Unmarshal(raw, &stored); err != nil || stored.ServerURL != want || stored.Tokens != saved.Tokens {
				t.Fatal("normalized URL and tokens were not persisted", err)
			}
		})
	}
}

func TestOfficialURLMigrationDoesNotRepairInvalidAddresses(t *testing.T) {
	for _, address := range []string{previousOfficialServerURL + "/api", "http://api-hqip.sharevps.online"} {
		if migrateOfficialServerURL(address) != address {
			t.Fatal("invalid URL became an official root", address)
		}
		if _, err := NewAPI(address, Tokens{}); err == nil {
			t.Fatal("invalid server URL accepted", address)
		}
	}
}

func TestFreshInstallUsesCurrentOfficialAPI(t *testing.T) {
	c, err := New(t.TempDir(), fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.View().ServerURL != "https://hqip.sharevps.online" || c.View().LoggedIn {
		t.Fatal("unexpected first-install endpoint or identity")
	}
}

func TestOfficialControlDomainsBypassProxyAndTUNDNS(t *testing.T) {
	profile := Profile{Nodes: []Node{{ID: "1", Server: "203.0.113.7", SNI: "node.example.com", Port: 443, Password: "test-password", Ready: true}}}
	for _, tun := range []bool{false, true} {
		for _, address := range []string{DefaultServerURL, DefaultServerURL + "/", previousOfficialServerURL, previousOfficialServerURL + "/", legacyServerURL, legacyUserSiteURL, "https://custom.example", "https://api-hqip.sharevps.online.evil.example", "https://api-hqip.sharevps.online:444"} {
			data, err := GenerateConfig(ConfigOptions{Profile: profile, Bundle: rules.Default(), Selected: "1", Mode: "global", TUN: tun, MixedPort: 14001, APIPort: 14002, APISecret: "01234567890123456789012345678901", ServerURL: address})
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Route struct {
					Rules []struct {
						Domain   []string `json:"domain"`
						Outbound string   `json:"outbound"`
					} `json:"rules"`
				} `json:"route"`
				DNS struct {
					Rules []struct {
						Domain []string `json:"domain"`
						Server string   `json:"server"`
					} `json:"rules"`
				} `json:"dns"`
			}
			if err = json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			direct, local := map[string]bool{}, map[string]bool{}
			for _, rule := range config.Route.Rules {
				if rule.Outbound == "direct" {
					for _, domain := range rule.Domain {
						direct[domain] = true
					}
				}
			}
			for _, rule := range config.DNS.Rules {
				if rule.Server == "local" {
					for _, domain := range rule.Domain {
						local[domain] = true
					}
				}
			}
			want := map[string]bool{"203.0.113.7": true}
			if address == DefaultServerURL || address == DefaultServerURL+"/" || address == previousOfficialServerURL || address == previousOfficialServerURL+"/" {
				want["hqip.sharevps.online"] = true
			} else {
				u, err := url.Parse(address)
				if err != nil {
					t.Fatal(err)
				}
				want[u.Hostname()] = true
			}
			if !reflect.DeepEqual(direct, want) || !reflect.DeepEqual(local, want) {
				t.Fatalf("unexpected bypass domains for %s (TUN %v): direct %v, DNS %v, want %v", address, tun, direct, local, want)
			}
		}
	}
}

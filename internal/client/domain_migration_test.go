package client

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestOfficialAuthorizationOriginPair(t *testing.T) {
	api, _ := url.Parse("https://api-hqip.5202345.xyz")
	for _, v := range []struct {
		url string
		ok  bool
	}{
		{"https://hqip.5202345.xyz/authorize?request=abc", true},
		{"https://hqip.5202345.xyz.evil.example/authorize", false},
		{"https://hqip.5202345.xyz:444/authorize", false},
		{"http://hqip.5202345.xyz/authorize", false},
		{"https://cms-hqip.5202345.xyz/authorize", false},
		{"https://hqip.5202345.xyz/login", false},
	} {
		page, _ := url.Parse(v.url)
		if trustedAuthorizationOrigin(api, page) != v.ok {
			t.Fatal(v.url)
		}
	}
	custom, _ := url.Parse("https://custom.example")
	page, _ := url.Parse("https://hqip.5202345.xyz/authorize")
	if trustedAuthorizationOrigin(custom, page) {
		t.Fatal("custom server gained official origin trust")
	}
}

func TestSavedOfficialURLMigrationLeavesCustomServerAndTokens(t *testing.T) {
	for _, address := range []string{"https://hqip.5202345.xyz", "https://hqip.5202345.xyz/", "https://custom.example"} {
		dir := t.TempDir()
		saved := persisted{ServerURL: address, AccountVersion: 2, Mode: "rule", Tokens: Tokens{RefreshToken: "stored-refresh", AccessToken: "stored-access"}}
		b, _ := json.Marshal(saved)
		if err := os.WriteFile(filepath.Join(dir, "session.dat"), b, 0600); err != nil {
			t.Fatal(err)
		}
		c, err := New(dir, fakePlatform{}, &fakeEngine{})
		if err != nil {
			t.Fatal(err)
		}
		want := "https://api-hqip.5202345.xyz"
		if address == "https://custom.example" {
			want = address
		}
		if c.saved.ServerURL != want || c.api.Tokens().RefreshToken != "stored-refresh" {
			t.Fatal("migration changed identity or custom URL")
		}
	}
}

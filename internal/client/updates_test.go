package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppVersionInjection(t *testing.T) {
	if normalizedVersion(AppVersion) == "" {
		t.Fatalf("invalid application version %q", AppVersion)
	}
	if want := os.Getenv("HQIP_EXPECT_APP_VERSION"); want != "" && AppVersion != want {
		t.Fatalf("version linker injection: got %q want %q", AppVersion, want)
	}
}

func TestUpdateVersionOrdering(t *testing.T) {
	for _, pair := range [][2]string{{"4.3.0", "4.10.0"}, {"4.3.0-rc.1", "4.3.0"}, {"4.3.0-rc.9", "4.3.0-rc.10"}, {"4.3.0", "5.0.0"}} {
		if compareVersion(pair[0], pair[1]) >= 0 {
			t.Fatal(pair)
		}
	}
	for _, value := range []string{"", "4", "4.3", "04.3.0", "4.3.0.1", "4.3.0-01", "4.3.0\n"} {
		if normalizedVersion(value) != "" {
			t.Fatal("accepted invalid version", value)
		}
	}
	if compareVersion("v4.3.0+first", "4.3.0+second") != 0 || versionIdentity("v4.3.0+first") != versionIdentity("4.3.0+second") {
		t.Fatal("build metadata changed version identity")
	}
	if hasPrerelease("4.3.0+build-10") || !hasPrerelease("4.3.0-rc.10+build") {
		t.Fatal("misclassified prerelease metadata")
	}
}

func releaseFixture(base, version string) updateRelease {
	hash := strings.Repeat("a", 64)
	return updateRelease{Version: version, Platform: "windows-amd64", Filename: "HQIP-Windows-x64.zip", SHA256: hash, Size: 100, URL: base + "/downloads/" + hash + "/HQIP-Windows-x64.zip"}
}
func updaterFixture(t *testing.T, dir string, handler http.HandlerFunc) (*UpdateChecker, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	u := NewUpdateChecker(dir, "4.3.0", func() string { return server.URL })
	u.http.Transport = server.Client().Transport
	t.Cleanup(u.Close)
	return u, server
}
func sendRelease(w http.ResponseWriter, r updateRelease) {
	_ = json.NewEncoder(w).Encode(map[string]any{"releases": []updateRelease{r}})
}

func TestUpdateChecksValidateReleaseAndNeverSendCredentials(t *testing.T) {
	var base string
	for _, test := range []struct {
		name            string
		alter           func(*updateRelease)
		available, fail bool
	}{
		{"new", func(*updateRelease) {}, true, false},
		{"same", func(r *updateRelease) { r.Version = "v4.3.0+rebuild" }, false, false},
		{"old", func(r *updateRelease) { r.Version = "4.2.0" }, false, false},
		{"prerelease", func(r *updateRelease) { r.Version = "5.0.0-rc.1" }, false, false},
		{"cross-origin", func(r *updateRelease) { r.URL = "https://untrusted.invalid/downloads/" + r.SHA256 + "/" + r.Filename }, false, true},
		{"legacy-path", func(r *updateRelease) { r.URL = base + "/downloads/" + r.Filename }, false, true},
		{"wrong-hash", func(r *updateRelease) { r.SHA256 = strings.Repeat("b", 64) }, false, true},
		{"query", func(r *updateRelease) { r.URL += "?redirect=bad" }, false, true},
		{"dirty", func(r *updateRelease) { r.Dirty = true }, false, true},
		{"version", func(r *updateRelease) { r.Version = "broken" }, false, true},
		{"filename", func(r *updateRelease) { r.Filename = "bad.exe" }, false, true},
		{"oversize", func(r *updateRelease) { r.Size = 501 << 20 }, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, s := updaterFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/user/releases" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("unexpected request")
				}
				release := releaseFixture(base, "4.4.0")
				test.alter(&release)
				sendRelease(w, release)
			})
			base = s.URL
			err := u.Check(false)
			if (err != nil) != test.fail {
				t.Fatalf("error=%v", err)
			}
			v := u.View()
			if v.Available != test.available || v.Prompt != test.available || v.Checking {
				t.Fatal(v)
			}
			url, err := u.DownloadURL()
			if test.available {
				if err != nil || url != releaseFixture(base, "4.4.0").URL {
					t.Fatal(url, err)
				}
			} else if err == nil {
				t.Fatal("download without trusted release")
			}
		})
	}
}

func TestUpdateFailureClearsPreviouslyTrustedDownload(t *testing.T) {
	var base string
	var fail atomic.Bool
	u, _ := updaterFixture(t, t.TempDir(), func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		sendRelease(w, releaseFixture(base, "4.4.0"))
	})
	base = u.source()
	if err := u.Check(false); err != nil || !u.View().Prompt {
		t.Fatalf("initial release: %v %+v", err, u.View())
	}
	fail.Store(true)
	if err := u.Check(false); err == nil {
		t.Fatal("expected service failure")
	}
	v := u.View()
	if v.Available || v.Prompt || v.Error == "" {
		t.Fatal("stale release survived service failure", v)
	}
	if _, err := u.DownloadURL(); err == nil {
		t.Fatal("stale download link remained available")
	}
}

func TestUpdateReminderPersistsAndNewVersionsPrompt(t *testing.T) {
	dir := t.TempDir()
	var version atomic.Value
	version.Store("4.4.0")
	var base string
	u, s := updaterFixture(t, dir, func(w http.ResponseWriter, r *http.Request) {
		sendRelease(w, releaseFixture(base, version.Load().(string)))
	})
	base = s.URL
	if err := u.Check(false); err != nil {
		t.Fatal(err)
	}
	if err := u.Snooze(); err != nil {
		t.Fatal(err)
	}
	if u.View().Prompt {
		t.Fatal("snooze did not suppress prompt")
	}
	u.Close()
	restarted := NewUpdateChecker(dir, "4.3.0", func() string { return base })
	restarted.http.Transport = s.Client().Transport
	t.Cleanup(restarted.Close)
	version.Store("v4.4.0+rebuild")
	if err := restarted.Check(false); err != nil {
		t.Fatal(err)
	}
	if restarted.View().Prompt {
		t.Fatal("same version prompted after restart")
	}
	version.Store("4.5.0")
	if err := restarted.Check(false); err != nil {
		t.Fatal(err)
	}
	if !restarted.View().Prompt {
		t.Fatal("new version inherited snooze")
	}
	if err := restarted.Snooze(); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Check(true); err != nil {
		t.Fatal(err)
	}
	if !restarted.View().Prompt {
		t.Fatal("manual check suppressed update")
	}
	if err := restarted.Snooze(); err != nil {
		t.Fatal(err)
	}
	restarted.mu.Lock()
	restarted.reminder.Until = time.Now().Add(-time.Second)
	restarted.mu.Unlock()
	if !restarted.View().Prompt {
		t.Fatal("cooldown never expired")
	}
}

func TestUpdateServiceFailuresAndTLSAreSafe(t *testing.T) {
	for _, kind := range []string{"status", "redirect", "json", "oversize", "tls", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			u, s := updaterFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch kind {
				case "status":
					w.WriteHeader(503)
				case "redirect":
					http.Redirect(w, r, "https://example.invalid/steal", 302)
				case "json":
					w.Write([]byte("invalid"))
				case "oversize":
					w.Write([]byte(strings.Repeat("x", 65537)))
				case "timeout":
					<-r.Context().Done()
				default:
					sendRelease(w, releaseFixture("https://invalid", "4.4.0"))
				}
			})
			if kind == "tls" {
				u.http.Transport = http.DefaultTransport.(*http.Transport).Clone()
			}
			if kind == "timeout" {
				u.http.Timeout = 30 * time.Millisecond
			}
			if err := u.Check(false); err == nil {
				t.Fatal("unexpected success", s.URL)
			}
			v := u.View()
			if v.Available || v.Prompt || v.Error == "" || v.Checking {
				t.Fatal(v)
			}
			if kind != "tls" && calls.Load() != 1 {
				t.Fatal("redirect followed or request retried")
			}
		})
	}
}

func TestUpdateCloseCancelsAndJoinsRequest(t *testing.T) {
	started := make(chan struct{})
	u, _ := updaterFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	done := make(chan struct{})
	go func() { defer close(done); _ = u.Check(false) }()
	<-started
	u.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not join request")
	}
	if err := u.Check(false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUpdateSourceChangesDiscardInflightResponse(t *testing.T) {
	started := make(chan struct{})
	var source string
	var sourceMu sync.Mutex
	u, _ := updaterFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	source = u.source()
	u.source = func() string { sourceMu.Lock(); defer sourceMu.Unlock(); return source }
	done := make(chan struct{})
	go func() { defer close(done); _ = u.Check(false) }()
	<-started
	sourceMu.Lock()
	source = "https://new.invalid"
	sourceMu.Unlock()
	if v := u.View(); v.Available || v.Checking || v.CheckedAt != nil {
		t.Fatal(v)
	}
	<-done
	if v := u.View(); v.Available || v.Error != "" {
		t.Fatal("old result published", v)
	}
}

func TestUpdateRunChecksAtStartupAndNotifiesOnlyOnce(t *testing.T) {
	var base string
	var calls atomic.Int32
	u, s := updaterFixture(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		sendRelease(w, releaseFixture(base, "4.4.0"))
	})
	base = s.URL
	notified := make(chan struct{}, 5)
	done := make(chan struct{})
	go func() { defer close(done); u.Run(func() { notified <- struct{}{} }) }()
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("no startup notification")
	}
	for i := 0; i < 3; i++ {
		u.wake <- struct{}{}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 || len(notified) != 0 {
		t.Fatal("polling repeated request or notification")
	}
	u.Close()
	<-done
}

package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBundleSourceAvailableBeforeLoginAndAfterRestart(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	if c.View().LoggedIn || c.BundleUpdateSource() != DefaultServerURL {
		t.Fatal("fresh installation cannot check the official update channel")
	}
	c.Close()
	// A configured private origin must survive restart even without tokens.
	raw, _ := json.Marshal(persisted{AccountVersion: 3, ServerURL: "https://custom.example", Mode: "rule"})
	if err = os.WriteFile(filepath.Join(dir, "session.dat"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err = New(dir, fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.View().LoggedIn || c.BundleUpdateSource() != "https://custom.example" {
		t.Fatal("logged-out restart changed the configured origin")
	}
}

func TestBundleStartupDownloadsWithoutAccount(t *testing.T) {
	f := newBundleFixture(t)
	c, err := New(t.TempDir(), fakePlatform{}, &fakeEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.saved.ServerURL = f.source
	u := f.updater()
	u.source = c.BundleUpdateSource
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); u.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for !u.View().Ready && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !u.View().Ready || c.View().LoggedIn || c.api != nil || c.View().RunState != "disconnected" {
		t.Fatal("startup update required an account or changed connection state")
	}
	cancel()
	<-done
	if f.downloads != 1 || f.stages != 1 {
		t.Fatal("startup did not download and stage exactly once")
	}
}

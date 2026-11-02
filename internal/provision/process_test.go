package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if marker := os.Getenv("HQIP_TEST_PROBE_MARKER"); marker != "" {
		for i, arg := range os.Args {
			if arg == "--config" && i+1 < len(os.Args) {
				_ = os.WriteFile(marker, []byte(os.Args[i+1]), 0600)
			}
		}
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(m.Run())
}

func TestProbeCancellationReapsProcessAndDeletesCredentialFiles(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("unsupported probe platform")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	t.Setenv("HQIP_TEST_PROBE_MARKER", marker)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	name, err := probeArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	manifest, _ := json.Marshal(map[string]any{"sha256": map[string]string{name: hex.EncodeToString(hash[:])}})
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := New(Options{ArtifactDir: dir}).Verify(ctx, Input{Node: validNode(), ProbePassword: "test-probe-credential-long-enough"}, nil)
		done <- err
	}()
	var config []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		config, err = os.ReadFile(marker)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(config) == 0 {
		t.Fatal("probe helper did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled probe reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe process was not reaped after cancellation")
	}
	if _, err := os.Stat(filepath.Dir(string(config))); !os.IsNotExist(err) {
		t.Fatal("probe directory/config/password retained after cancellation")
	}
}

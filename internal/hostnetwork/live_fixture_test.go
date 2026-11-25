package hostnetwork

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in verification of read-only Linux snapshots captured by operations. The
// fixture root contains one directory per host with proc/ and net/ trees; no
// credentials or operational evidence are persisted in the business database.
func TestHostNetworkCapturedLinuxFixtures(t *testing.T) {
	root := os.Getenv("HQIP_HOST_NETWORK_FIXTURES")
	if root == "" {
		t.Skip("set HQIP_HOST_NETWORK_FIXTURES to captured Linux snapshots")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		count++
		t.Run(entry.Name(), func(t *testing.T) {
			host := filepath.Join(root, entry.Name())
			stamp, err := os.ReadFile(filepath.Join(host, "at"))
			if err != nil {
				t.Fatal(err)
			}
			at, err := time.Parse(time.RFC3339Nano, string(stamp))
			if err != nil {
				t.Fatal(err)
			}
			sample, err := readAt(filepath.Join(host, "proc"), filepath.Join(host, "net"), at)
			if err != nil || len(sample.Interfaces) == 0 {
				t.Fatal(sample, err)
			}
			t.Logf("carriers=%d excluded=%v", len(sample.Interfaces), sample.Excluded)
		})
	}
	if count == 0 {
		t.Fatal("empty capture directory")
	}
}

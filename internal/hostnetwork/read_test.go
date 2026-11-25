package hostnetwork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hostFixture(t *testing.T) (string, string, time.Time) {
	t.Helper()
	root := t.TempDir()
	proc, net := filepath.Join(root, "proc"), filepath.Join(root, "net")
	files := map[string]string{
		"proc/sys/kernel/random/boot_id": "11111111-1111-1111-1111-111111111111\n",
		"proc/stat":                      "cpu 1 2 3 4\nbtime 1700000000\n",
		"proc/net/dev":                   "Inter-| Receive | Transmit\n lo: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0\n eth0: 100 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0\n bond0: 100 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0\n veth0: 80 1 0 0 0 0 0 0 180 1 0 0 0 0 0 0\n",
		"net/eth0/ifindex":               "2\n",
		"net/eth0/device/model":          "virtio",
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return proc, net, time.Unix(1700001000, 0)
}

func TestHostNetworkCountsCarrierOnceAndIncludesUnattributedTraffic(t *testing.T) {
	proc, net, now := hostFixture(t)
	s, err := readAt(proc, net, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Interfaces) != 1 || s.Interfaces[0].Receive != 100 || s.Interfaces[0].Send != 200 || len(s.Excluded) != 3 {
		t.Fatalf("wrong NIC boundary: %+v", s)
	}
	// A system-only transfer changes these counters without any proxy identity.
	path := filepath.Join(proc, "net/dev")
	data, _ := os.ReadFile(path)
	data = []byte(strings.Replace(string(data), "eth0: 100", "eth0: 123", 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	next, err := readAt(proc, net, now.Add(time.Second))
	if err != nil || next.Interfaces[0].Receive-s.Interfaces[0].Receive != 23 {
		t.Fatal(next, err)
	}
}

func TestHostNetworkMissingOrInvalidCountersAreNotZero(t *testing.T) {
	for _, bad := range []string{"negative", "overflow", "missing", "no-carrier"} {
		t.Run(bad, func(t *testing.T) {
			proc, net, now := hostFixture(t)
			switch bad {
			case "missing":
				os.Remove(filepath.Join(net, "eth0/ifindex"))
			case "no-carrier":
				os.RemoveAll(filepath.Join(net, "eth0/device"))
			default:
				value := "-1"
				if bad == "overflow" {
					value = "9223372036854775808"
				}
				os.WriteFile(filepath.Join(proc, "net/dev"), []byte("eth0: "+value+" 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0\n"), 0600)
			}
			if _, err := readAt(proc, net, now); err == nil {
				t.Fatal("unreadable counter treated as measured zero")
			}
		})
	}
}

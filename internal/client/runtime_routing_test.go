package client

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
)

// Validate each emitted configuration with the exact packaged core. `check`
// parses and validates only; it does not install a TUN or change system proxy.
func TestPinnedCoreRoutingMatrix(t *testing.T) {
	binary := os.Getenv("HQIP_SINGBOX_EXE")
	if binary == "" {
		t.Skip("set HQIP_SINGBOX_EXE for packaged-core config validation")
	}
	for _, mode := range []string{"rule", "global"} {
		for _, tun := range []bool{false, true} {
			body, err := GenerateConfig(ConfigOptions{
				Profile: Profile{Nodes: []Node{{ID: "n", Server: "node.example", SNI: "node.example", Port: 443, Password: "test-secret", Ready: true}}},
				Bundle:  rules.Default(), Selected: "n", Mode: mode, TUN: tun,
				MixedPort: 14001, APIPort: 14002, ProbePort: 14003, ServerURL: DefaultServerURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "config.json")
			if err = os.WriteFile(file, body, 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(binary, "check", "-c", file).CombinedOutput(); err != nil {
				t.Fatalf("%s TUN=%v rejected by packaged core: %v\n%s", mode, tun, err, output)
			}
		}
	}
}

// New and old Windows hosts share the same rule/global and proxy/TUN matrix.
// This checks route ordering, not merely whether a process name appears in JSON.
func TestRuntimeManagementBypassPrecedesPauseInEveryMode(t *testing.T) {
	for _, mode := range []string{"rule", "global"} {
		for _, tun := range []bool{false, true} {
			body, err := GenerateConfig(ConfigOptions{
				Profile: Profile{Nodes: []Node{{ID: "n", Server: "node.example", SNI: "node.example", Port: 443, Password: "test-secret", Ready: true}}},
				Bundle:  rules.Default(), Selected: "n", Mode: mode, TUN: tun,
				MixedPort: 14001, APIPort: 14002, ProbePort: 14003, ServerURL: DefaultServerURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Route struct {
					Rules []json.RawMessage `json:"rules"`
				} `json:"route"`
			}
			if err = json.Unmarshal(body, &config); err != nil {
				t.Fatal(err)
			}
			found, paused := false, false
			for _, rule := range config.Route.Rules {
				if bytes.Contains(rule, []byte(suspendedClashMode)) {
					paused = true
				}
				if bytes.Contains(rule, []byte("hqip-runtime.exe")) {
					found = true
					if !tun || paused || !bytes.Contains(rule, []byte(`"direct"`)) || !bytes.Contains(rule, []byte(`"tun-in"`)) {
						t.Fatalf("management bypass ordering/scope is invalid for %s TUN=%v", mode, tun)
					}
				}
			}
			if found != tun || !paused {
				t.Fatalf("missing expected route for %s TUN=%v", mode, tun)
			}
		}
	}
	if !managementConnection("", "", `C:\HQIP\versions\bundled\hqip-runtime.exe`, Profile{}, "") {
		t.Fatal("recovery must preserve companion management connections")
	}
}
